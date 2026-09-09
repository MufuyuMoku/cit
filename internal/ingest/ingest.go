package ingest

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/MufuyuMoku/cit/internal/store"
)

// Vault is the part of the content-addressed store that ingest needs. Declared
// here rather than imported as a concrete type so the debounce logic can be
// tested without a real vault on disk.
type Vault interface {
	Store(ctx context.Context, r io.Reader) (string, error)
	Release(fileHash string) error
}

// PreviewMaker renders the thumbnail for a piece of content. Optional: with no
// maker configured, versions are recorded without pictures.
//
// Anything it returns is advisory. A thumbnail that cannot be drawn is a
// missing picture, never a missing version — see makePreview.
type PreviewMaker interface {
	Generate(ctx context.Context, fileHash, path string) (store.Preview, error)
}

// Defaults chosen for how design tools actually behave. A Photoshop save of a
// large PSD can take several seconds and touches the file repeatedly; the quiet
// period has to outlast that, or one Ctrl+S becomes five versions.
const (
	DefaultQuietPeriod  = 3 * time.Second
	DefaultPollInterval = time.Second

	// A big file gets a proportionally longer quiet period. A video export
	// writes in bursts with encoding gaps between them, and a gap can easily
	// outlast three seconds; treating that pause as "finished" versions a
	// half-written file, which the user has no way to notice. Scaling by size
	// does not remove the guess, it puts it somewhere defensible.
	DefaultQuietScale = 10 << 20 // one extra second of patience per 10 MiB

	// ...but not without end. Past this, waiting stops being caution and starts
	// being a file that never gets versioned at all.
	DefaultMaxQuietPeriod = 5 * time.Minute

	// How long a file may go without its content actually being read before it
	// is hashed again regardless of what size and mtime claim.
	DefaultVerifyInterval = time.Hour

	// How many times to retry a file that will not open for a reason other than
	// being locked, before giving up and reporting it.
	maxOpenAttempts = 5
)

// Ingester watches folders and turns each settled save into one version.
type Ingester struct {
	db    *sql.DB
	vault Vault

	quietPeriod    time.Duration
	maxQuietPeriod time.Duration
	quietScale     int64
	pollInterval   time.Duration
	verifyInterval time.Duration
	now            func() time.Time

	previews PreviewMaker

	// checkOpenable is a field so tests can drive the operating-system answers
	// that are otherwise impossible to provoke portably: a locked file on Linux,
	// or a drive that went away mid-scan.
	checkOpenable func(path string) (openState, error)

	// sightings is the debounce state, keyed by path. It lives across scans:
	// a file is only processed once its size and mtime have held still for
	// quietPeriod, which is how a save that dribbles out over several seconds
	// still produces exactly one version.
	sightings map[string]sighting

	// openFailures counts consecutive opens that failed for a reason other than
	// the file being locked. A locked file is waited on indefinitely, which is
	// right; anything else gets a bounded number of tries and is then reported,
	// because a file on a disconnected drive must not wait forever with no
	// trace.
	openFailures map[string]int

	// problems holds paths that have given up, so a caller can show them.
	problems map[string]Problem

	// vanishedSince records when a tracked path was first found missing.
	//
	// A path that disappears is not forgotten straight away. Half of what looks
	// like a deletion is really the first half of a rename, and the tracking row
	// is the only evidence that ties the old name to the new one. Dropping it
	// immediately turns every rename into a brand new asset with no history.
	vanishedSince map[string]time.Time
}

// sighting is what the last scan saw at a path.
type sighting struct {
	size        int64
	modTime     time.Time
	stableSince time.Time
}

// Option configures an Ingester.
type Option func(*Ingester)

// WithQuietPeriod sets how long a file must stop changing before it is read.
func WithQuietPeriod(d time.Duration) Option {
	return func(i *Ingester) { i.quietPeriod = d }
}

// WithPollInterval sets how often Watch rescans.
func WithPollInterval(d time.Duration) Option {
	return func(i *Ingester) { i.pollInterval = d }
}

// WithClock replaces the time source. Tests use it to step over the quiet
// period without sleeping.
func WithClock(now func() time.Time) Option {
	return func(i *Ingester) { i.now = now }
}

// WithQuietScale sets how many bytes buy one extra second of quiet period.
// Zero disables scaling, leaving the flat quiet period.
func WithQuietScale(bytesPerSecond int64) Option {
	return func(i *Ingester) { i.quietScale = bytesPerSecond }
}

// WithMaxQuietPeriod caps how far size scaling may stretch the wait.
func WithMaxQuietPeriod(d time.Duration) Option {
	return func(i *Ingester) { i.maxQuietPeriod = d }
}

// WithVerifyInterval sets how stale a file's last real read may get before it
// is hashed again regardless of its size and mtime. Zero disables the pass.
func WithVerifyInterval(d time.Duration) Option {
	return func(i *Ingester) { i.verifyInterval = d }
}

// WithPreviews attaches a thumbnail generator. Without one, versions are still
// recorded; they simply have no picture.
func WithPreviews(p PreviewMaker) Option {
	return func(i *Ingester) { i.previews = p }
}

// Problem is a file ingest could not read and has stopped retrying.
//
// These are surfaced rather than swallowed. A file that quietly never reaches
// the timeline is the same class of failure as a half-written one that does.
type Problem struct {
	Path     string
	Attempts int
	Since    time.Time
	Err      error
}

// Problems returns the files ingest has given up on, sorted by path. A file
// leaves the list as soon as it changes on disk or opens successfully.
func (i *Ingester) Problems() []Problem {
	out := make([]Problem, 0, len(i.problems))
	for _, p := range i.problems {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Problem) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// New returns an Ingester writing to db and vault.
func New(db *sql.DB, v Vault, opts ...Option) *Ingester {
	i := &Ingester{
		db:             db,
		vault:          v,
		quietPeriod:    DefaultQuietPeriod,
		maxQuietPeriod: DefaultMaxQuietPeriod,
		quietScale:     DefaultQuietScale,
		pollInterval:   DefaultPollInterval,
		verifyInterval: DefaultVerifyInterval,
		now:            time.Now,
		checkOpenable:  checkOpenable,
		sightings:      map[string]sighting{},
		openFailures:   map[string]int{},
		problems:       map[string]Problem{},
		vanishedSince:  map[string]time.Time{},
	}
	for _, opt := range opts {
		opt(i)
	}
	return i
}

// Result reports what one scan did.
type Result struct {
	// Settled counts files that had stopped changing and were examined.
	Settled int
	// NewVersions counts versions actually recorded.
	NewVersions int
	// NewAssets counts works seen for the first time.
	NewAssets int
	// Renamed counts files recognised as the same content at a new path.
	Renamed int
	// Waiting counts files still changing, or still held open by their writer.
	Waiting int
	// Vanished counts tracked paths that are no longer on disk.
	Vanished int
	// Verified counts files re-read on schedule and found unchanged after all.
	Verified int
	// Unreadable counts files that have exhausted their open attempts and are
	// now listed by Problems.
	Unreadable int
	// PreviewsMade counts thumbnails generated this scan, successful or not —
	// every one of them recorded an outcome.
	PreviewsMade int
	// PreviewsSkipped counts thumbnail attempts that could not even be
	// recorded. The version is still on the timeline; only its picture is
	// missing.
	PreviewsSkipped int
}

// Scan walks roots once and records a version for every file that has settled
// since the last scan.
//
// ctx reaches vault.Store, so cancelling it mid-file aborts the store rather
// than finishing a 200 MB read nobody is waiting for.
func (i *Ingester) Scan(ctx context.Context, roots ...string) (Result, error) {
	var result Result

	seen, err := i.walk(ctx, roots)
	if err != nil {
		return result, err
	}

	// Sort for a stable, reproducible processing order. Map iteration order
	// would make a failure impossible to reproduce.
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	slices.Sort(paths)

	now := i.now()

	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		info := seen[path]
		if !i.settled(path, info, now) {
			result.Waiting++
			continue
		}
		result.Settled++

		if _, given := i.problems[path]; given {
			// Already reported as unreadable and unchanged since. Do not spin
			// on it every second.
			result.Unreadable++
			result.Settled--
			continue
		}

		read, err := i.worthReading(ctx, path, info, now)
		if err != nil {
			return result, err
		}
		if !read {
			continue
		}

		outcome, err := i.ingestFile(ctx, path, info, seen, now, &result)
		if err != nil {
			if errors.Is(err, errStillBusy) {
				// Held open by its writer. Try again next scan rather than
				// reading a half-written file.
				result.Waiting++
				result.Settled--
				continue
			}
			if errors.Is(err, errUnreadable) {
				result.Unreadable++
				result.Settled--
				continue
			}
			return result, err
		}
		switch outcome {
		case outcomeNewVersion:
			result.NewVersions++
		case outcomeNewAsset:
			result.NewAssets++
			result.NewVersions++
		case outcomeRenamed:
			result.Renamed++
		case outcomeVerified:
			result.Verified++
		case outcomeMoved:
			// The file changed under us while we were reading it. Leave it for
			// the next scan, when it will have settled again.
			result.Waiting++
			result.Settled--
		}
	}

	vanished, err := i.reconcileVanished(ctx, roots, seen, now)
	if err != nil {
		return result, err
	}
	result.Vanished = vanished

	return result, nil
}

// Watch rescans until ctx is cancelled, then returns ctx.Err().
//
// Cancellation propagates into vault.Store, so closing the application while a
// large file is being ingested aborts that store cleanly and leaves nothing
// behind.
func (i *Ingester) Watch(ctx context.Context, roots ...string) error {
	ticker := time.NewTicker(i.pollInterval)
	defer ticker.Stop()

	for {
		if _, err := i.Scan(ctx, roots...); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// walk collects every candidate file under roots.
func (i *Ingester) walk(ctx context.Context, roots []string) (map[string]fs.FileInfo, error) {
	seen := map[string]fs.FileInfo{}

	for _, root := range roots {
		root = filepath.Clean(root)

		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				// An unreadable subdirectory must not abort the whole scan; the
				// rest of the user's work still deserves to be tracked.
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}

			if d.IsDir() {
				if path != root && shouldSkipDir(d.Name()) {
					return fs.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			if shouldSkip(path) {
				return nil
			}

			info, err := d.Info()
			if err != nil {
				return nil // vanished between listing and stat
			}
			seen[path] = info
			return nil
		})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			return nil, fmt.Errorf("ingest: telusuri %s: %w", root, err)
		}
	}

	return seen, nil
}

// settled reports whether a file has held still long enough to be read, and
// updates the debounce state.
//
// Both size and mtime are watched. Size alone misses an in-place edit that
// keeps the length the same, which is exactly what a small tweak to a large
// PSD looks like.
func (i *Ingester) settled(path string, info fs.FileInfo, now time.Time) bool {
	prev, known := i.sightings[path]
	current := sighting{size: info.Size(), modTime: info.ModTime()}

	if !known || prev.size != current.size || !prev.modTime.Equal(current.modTime) {
		current.stableSince = now
		i.sightings[path] = current
		// It moved, so whatever was wrong with it before may not be any more.
		delete(i.problems, path)
		delete(i.openFailures, path)
		return false
	}

	// Unchanged since last time: keep the original stableSince.
	return now.Sub(prev.stableSince) >= i.quietFor(info.Size())
}

// quietFor returns how long a file of this size must hold still.
//
// The flat period is fine for a PSD. It is not fine for a two-hour video
// export, which pauses for whole minutes between flushes while the encoder
// works; the flat period would call each pause the end of the file. Scaling
// with size is a heuristic, but it is the only signal available on platforms
// with no mandatory locking, and it is the only thing protecting Linux and
// macOS users here at all.
func (i *Ingester) quietFor(size int64) time.Duration {
	if i.quietScale <= 0 {
		return i.quietPeriod
	}
	scaled := time.Duration(size/i.quietScale) * time.Second
	if scaled < i.quietPeriod {
		return i.quietPeriod
	}
	if i.maxQuietPeriod > 0 && scaled > i.maxQuietPeriod {
		return i.maxQuietPeriod
	}
	return scaled
}

// worthReading decides whether to open and hash a file.
//
// The cheap signals — size and mtime — carry the normal case. They can also
// lie: some applications restore the original mtime after saving, and if the
// size matches too, a new version would be missed with no error and no trace.
// So a file whose content has not actually been read for verifyInterval is read
// again regardless of what those signals claim. Hashing every scan instead
// would mean re-reading gigabytes every second for a folder of video.
func (i *Ingester) worthReading(ctx context.Context, path string, info fs.FileInfo, now time.Time) (bool, error) {
	tracked, err := store.ObservedFileByPath(ctx, i.db, path)
	if errors.Is(err, store.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}

	if tracked.LastSize != info.Size() || !tracked.LastModifiedAt.Equal(info.ModTime()) {
		return true, nil
	}

	if i.verifyInterval > 0 && now.Sub(tracked.LastVerifiedAt) >= i.verifyInterval {
		return true, nil
	}
	return false, nil
}

type outcome int

const (
	outcomeNewVersion outcome = iota
	outcomeNewAsset
	outcomeRenamed
	outcomeMoved
	// outcomeVerified: read on schedule, content turned out to be exactly what
	// we already had. No new version, but the verification timestamp advances.
	outcomeVerified
)

// errStillBusy means the file could not be opened, almost always because its
// writer still holds it. Windows enforces that; on Unix the settled() check
// carries most of the weight.
var errStillBusy = errors.New("ingest: berkas masih dipegang penulisnya")

// errUnreadable means the file has failed to open too many times for a reason
// that is not a lock, and has been recorded in Problems.
var errUnreadable = errors.New("ingest: berkas tidak bisa dibaca")

// ingestFile stores one file and records what it is.
func (i *Ingester) ingestFile(
	ctx context.Context,
	path string,
	info fs.FileInfo,
	seen map[string]fs.FileInfo,
	now time.Time,
	res *Result,
) (outcome, error) {
	// Ask the operating system whether anyone else still holds the file. On
	// Windows that is a definitive answer and closes the half-written-export
	// hole outright; elsewhere it can only tell us the file is readable.
	state, openErr := i.checkOpenable(path)
	switch state {
	case openLocked:
		// A writer has it. Wait, without limit: an export legitimately holds
		// its output for an hour. Nothing is lost by being patient here.
		delete(i.openFailures, path)
		return 0, errStillBusy

	case openUnknown:
		if os.IsNotExist(openErr) {
			return outcomeMoved, nil
		}
		// Not locked, just unreadable: a disconnected drive, an unreadable
		// permission, a network share that went away. Retrying forever would
		// mean this file silently never reaches the timeline, so try a bounded
		// number of times and then say so out loud.
		i.openFailures[path]++
		if i.openFailures[path] >= maxOpenAttempts {
			i.problems[path] = Problem{
				Path:     path,
				Attempts: i.openFailures[path],
				Since:    now,
				Err:      openErr,
			}
			return 0, errUnreadable
		}
		return 0, errStillBusy
	}

	delete(i.openFailures, path)

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return outcomeMoved, nil
		}
		return 0, errStillBusy
	}

	fileHash, storeErr := i.vault.Store(ctx, bufio.NewReaderSize(f, 1<<20))
	f.Close()
	if storeErr != nil {
		return 0, storeErr
	}

	// The file may have changed while we were reading it — a save that took
	// longer than the quiet period, or a second save arriving mid-read. What we
	// hashed would then not match what is on disk, so record nothing and let
	// the next scan handle it once things are quiet again.
	after, err := os.Stat(path)
	if err != nil || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		if err := i.vault.Release(fileHash); err != nil {
			return 0, fmt.Errorf("ingest: lepaskan isi yang basi: %w", err)
		}
		delete(i.sightings, path)
		return outcomeMoved, nil
	}

	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("ingest: mulai transaksi: %w", err)
	}
	defer tx.Rollback()

	result, err := i.record(ctx, tx, path, info, fileHash, seen, now)
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("ingest: commit: %w", err)
	}

	// A rename stored content that was already there, so give back the extra
	// reference Store took. Done after the commit so a failure cannot leave the
	// catalogue pointing at content whose last reference we just dropped.
	if result == outcomeRenamed || result == outcomeVerified {
		// Both stored content that was already there, so give back the extra
		// reference Store took. After the commit, so a failure cannot leave the
		// catalogue pointing at content whose last reference we just dropped.
		if err := i.vault.Release(fileHash); err != nil {
			return 0, fmt.Errorf("ingest: lepaskan rujukan ganda: %w", err)
		}
	}

	// Draw the picture now, while the file is still on disk and has already
	// been proven to match the hash we just recorded. Waiting until later would
	// mean reading it again, and by then retention may have thinned the only
	// copy of that content away.
	i.makePreview(ctx, fileHash, path, res)

	return result, nil
}

// makePreview renders the thumbnail for content that has just been recorded.
//
// Nothing it does can fail the ingest. The version is already committed and on
// the timeline; a thumbnail that cannot be drawn — a corrupt psd, a missing
// ffmpeg, a full disk — leaves that version without a picture and nothing else.
// Losing a version because its picture could not be drawn would be a far worse
// trade than showing a generic icon.
func (i *Ingester) makePreview(ctx context.Context, fileHash, path string, res *Result) {
	if i.previews == nil {
		return
	}

	// Already drawn: two versions with identical content share one picture, and
	// re-rendering it every scan would be waste.
	if _, err := store.PreviewByFileHash(ctx, i.db, fileHash); err == nil {
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		res.PreviewsSkipped++
		return
	}

	if err := i.generateSafely(ctx, fileHash, path); err != nil {
		// Generate only returns an error when it could not even record the
		// outcome. There is nothing useful to do about it here beyond counting
		// it: the next scan will try again.
		res.PreviewsSkipped++
		return
	}
	res.PreviewsMade++
}

// generateSafely calls the thumbnail generator, surviving a panic inside it.
//
// The generator decodes hostile input — a deliberately malformed psd, a video
// container built to break parsers — and its own defences could have a hole. A
// panic there is a bug in drawing a picture; letting it unwind through here
// would abort the whole scan and cost the user versions of files that had
// nothing wrong with them.
func (i *Ingester) generateSafely(ctx context.Context, fileHash, path string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("ingest: pembuat gambar kecil panic pada %s: %v",
				filepath.Base(path), r)
		}
	}()

	_, err = i.previews.Generate(ctx, fileHash, path)
	return err
}

// record writes the catalogue rows for one settled file.
func (i *Ingester) record(
	ctx context.Context,
	tx store.DBTX,
	path string,
	info fs.FileInfo,
	fileHash string,
	seen map[string]fs.FileInfo,
	now time.Time,
) (outcome, error) {
	tracked, err := store.ObservedFileByPath(ctx, tx, path)
	switch {
	case err == nil:
		if tracked.LastHash == fileHash {
			// We read it because verification was due, and it turned out to be
			// exactly what we already had. No version — just record that the
			// content was genuinely checked, so the user can tell "verified
			// just now" from "assumed unchanged since Tuesday".
			tracked.LastSize = info.Size()
			tracked.LastModifiedAt = info.ModTime()
			tracked.LastSeenAt = now
			tracked.LastVerifiedAt = now
			if err := store.PutObservedFile(ctx, tx, tracked); err != nil {
				return 0, err
			}
			return outcomeVerified, nil
		}
		// Known path, new content: another save of the same work.
		return i.recordVersion(ctx, tx, tracked.AssetID, path, info, fileHash, now, outcomeNewVersion)

	case errors.Is(err, store.ErrNotFound):
		// New path. Before treating it as new work, check whether these exact
		// bytes just disappeared from somewhere else — that is a rename, and a
		// rename must not fork the history into a second asset.
		if source, ok, err := i.renameSource(ctx, tx, fileHash, seen); err != nil {
			return 0, err
		} else if ok {
			return i.applyRename(ctx, tx, source, path, info, now)
		}

		assetID, err := store.CreateAsset(ctx, tx, filepath.Base(path), now)
		if err != nil {
			return 0, err
		}
		return i.recordVersion(ctx, tx, assetID, path, info, fileHash, now, outcomeNewAsset)

	default:
		return 0, err
	}
}

// renameSource looks for a tracked path holding this content that is no longer
// on disk. That combination — same bytes, old path gone, new path appeared — is
// what a rename looks like from the outside.
func (i *Ingester) renameSource(
	ctx context.Context,
	tx store.DBTX,
	fileHash string,
	seen map[string]fs.FileInfo,
) (store.ObservedFile, bool, error) {
	candidates, err := store.ObservedFilesByHash(ctx, tx, fileHash)
	if err != nil {
		return store.ObservedFile{}, false, err
	}
	for _, c := range candidates {
		if _, stillThere := seen[c.Path]; !stillThere {
			return c, true, nil
		}
	}
	return store.ObservedFile{}, false, nil
}

// applyRename moves the tracking row and the asset's name to the new path. No
// version is recorded: the content did not change, only where it lives.
func (i *Ingester) applyRename(
	ctx context.Context,
	tx store.DBTX,
	source store.ObservedFile,
	newPath string,
	info fs.FileInfo,
	now time.Time,
) (outcome, error) {
	if err := store.DeleteObservedFile(ctx, tx, source.Path); err != nil {
		return 0, err
	}

	// A rename moves the file, not its history. Versions are tied to the file
	// by file_key, and the user's manual grouping judgements are tied to it by
	// path; both have to follow, or splitting an asset later would strand the
	// older versions and the decision would quietly stop applying.
	if err := store.RenameVersionFileKey(ctx, tx, source.Path, newPath); err != nil {
		return 0, err
	}
	if err := store.RenameGroupingDecisions(ctx, tx, source.Path, newPath); err != nil {
		return 0, err
	}

	delete(i.sightings, source.Path)
	delete(i.vanishedSince, source.Path)
	if err := store.PutObservedFile(ctx, tx, store.ObservedFile{
		Path:           newPath,
		AssetID:        source.AssetID,
		LastHash:       source.LastHash,
		LastSize:       info.Size(),
		LastModifiedAt: info.ModTime(),
		LastSeenAt:     now,
		LastVerifiedAt: now,
	}); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE assets SET name = ?, updated_at = ? WHERE id = ?`,
		filepath.Base(newPath), now.UnixNano(), source.AssetID); err != nil {
		return 0, fmt.Errorf("ingest: ganti nama karya: %w", err)
	}
	return outcomeRenamed, nil
}

func (i *Ingester) recordVersion(
	ctx context.Context,
	tx store.DBTX,
	assetID int64,
	path string,
	info fs.FileInfo,
	fileHash string,
	now time.Time,
	result outcome,
) (outcome, error) {
	if _, err := store.AddVersion(ctx, tx, store.Version{
		AssetID:    assetID,
		FileHash:   fileHash,
		Size:       info.Size(),
		ObservedAt: now,
		ModifiedAt: info.ModTime(),
		SourcePath: path,
		FileKey:    path,
	}); err != nil {
		return 0, err
	}
	if err := store.PutObservedFile(ctx, tx, store.ObservedFile{
		Path:           path,
		AssetID:        assetID,
		LastHash:       fileHash,
		LastSize:       info.Size(),
		LastModifiedAt: info.ModTime(),
		LastSeenAt:     now,
		LastVerifiedAt: now,
	}); err != nil {
		return 0, err
	}
	if err := store.TouchAsset(ctx, tx, assetID, now); err != nil {
		return 0, err
	}
	return result, nil
}

// reconcileVanished forgets tracked paths that are no longer on disk, but only
// after they have stayed gone for the quiet period.
//
// The delay is what makes rename detection possible: the tracking row for the
// old name has to outlive the moment the new name appears, or there is nothing
// left to match the content against. It also absorbs the brief gap an atomic
// save leaves when it unlinks the target before moving the replacement in.
//
// Only the tracking row goes. The asset and every version stay: a file being
// deleted or moved out of a watched folder does not erase what it was.
func (i *Ingester) reconcileVanished(
	ctx context.Context,
	roots []string,
	seen map[string]fs.FileInfo,
	now time.Time,
) (int, error) {
	count := 0
	for _, root := range roots {
		tracked, err := store.ObservedFilesUnder(ctx, i.db, filepath.Clean(root)+string(filepath.Separator))
		if err != nil {
			return count, err
		}
		for _, f := range tracked {
			if _, stillThere := seen[f.Path]; stillThere {
				delete(i.vanishedSince, f.Path)
				continue
			}

			since, noted := i.vanishedSince[f.Path]
			if !noted {
				i.vanishedSince[f.Path] = now
				continue
			}
			if now.Sub(since) < i.quietPeriod {
				continue
			}

			if err := store.DeleteObservedFile(ctx, i.db, f.Path); err != nil {
				return count, err
			}
			delete(i.sightings, f.Path)
			delete(i.vanishedSince, f.Path)
			count++
		}
	}
	return count, nil
}
