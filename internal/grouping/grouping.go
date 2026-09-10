package grouping

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/MufuyuMoku/cit/internal/store"
)

// Thumbnails is the part of the preview store grouping needs.
type Thumbnails interface {
	Open(fileHash string) (io.ReadCloser, error)
}

// Grouper decides which tracked files are the same work.
type Grouper struct {
	db     *sql.DB
	thumbs Thumbnails
	w      Weights
	now    func() time.Time

	// regroupMu admits one Regroup at a time.
	//
	// It guards Regroup and nothing else. Two passes in flight together would
	// each hold a clustering computed from a different catalogue and take turns
	// overwriting the other's conclusion, with whichever committed last
	// deciding. Split and Merge are deliberately not covered: they are single
	// short transactions, already serialised by the one database connection,
	// and making them wait behind a forty-second regrouping would turn a click
	// into a hang — worse, a Split invoked from inside a regrouping would
	// deadlock against it.
	regroupMu sync.Mutex

	// beforeApply runs after the scoring sweep and before the write
	// transaction, for tests that need to change the catalogue in exactly that
	// window. Never set in production.
	beforeApply func()

	// sweepTick runs once per row of the scoring sweep, so a test can cancel at
	// a known point inside it instead of sleeping and hoping. Never set in
	// production.
	sweepTick func(row int)
}

// Option configures a Grouper.
type Option func(*Grouper)

// WithWeights replaces the scoring weights and thresholds.
func WithWeights(w Weights) Option {
	return func(g *Grouper) { g.w = w }
}

// WithClock replaces the time source.
func WithClock(now func() time.Time) Option {
	return func(g *Grouper) { g.now = now }
}

// New returns a Grouper. thumbs may be nil, in which case grouping runs on
// names and save times alone.
func New(db *sql.DB, thumbs Thumbnails, opts ...Option) *Grouper {
	g := &Grouper{
		db:     db,
		thumbs: thumbs,
		w:      DefaultWeights(),
		now:    time.Now,
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// ErrStale means the catalogue changed while a regrouping pass was thinking, so
// the pass threw its conclusions away rather than writing them.
//
// This is a normal outcome, not a malfunction: a save landing or a user
// splitting two files by hand during a long pass is exactly what should
// invalidate it. Callers should try again rather than treat it as a failure.
var ErrStale = errors.New("grouping: katalog berubah saat pengelompokan berjalan, hasilnya dibuang")

// Result reports what one regrouping pass did.
type Result struct {
	// Files considered.
	Files int
	// Groups formed, counting singletons.
	Groups int
	// Moved counts files repointed at a different asset.
	Moved int
	// AssetsRemoved counts assets left empty and deleted.
	AssetsRemoved int
	// HashesComputed counts thumbnails hashed this pass.
	HashesComputed int
	// HonouredApart counts merges refused because the user had said no.
	HonouredApart int
}

// Regroup rebuilds the grouping over every tracked file.
//
// Grouping is applied straight away without asking. The golden rule says what
// can be undone should just be done, visibly and reversibly, and this can: the
// user can split anything back apart with one call, and that split is then
// permanent.
func (g *Grouper) Regroup(ctx context.Context) (Result, error) {
	g.regroupMu.Lock()
	defer g.regroupMu.Unlock()

	var result Result

	// Hashing thumbnails writes to previews and can take a while, so it happens
	// before the generation is recorded. Doing it afterwards would mean a long
	// hashing run regularly outlived its own snapshot and every pass aborted as
	// stale without ever converging. Its own read of the file list is cheap
	// next to the scoring that follows.
	files, err := store.AllObservedFiles(ctx, g.db)
	if err != nil {
		return result, err
	}
	if len(files) == 0 {
		return result, nil
	}
	hashed, err := g.ensureHashes(ctx, files)
	if err != nil {
		return result, err
	}
	result.HashesComputed = hashed

	// From here on the catalogue is a snapshot. The generation is read first,
	// before anything that depends on it: read the other way round, a change
	// landing between the rows and the counter would look unchanged at write
	// time and stale conclusions would be written. Reading it first can only
	// ever abort a pass that was in fact still valid, which costs a repeat.
	generation, err := store.GroupingGeneration(ctx, g.db)
	if err != nil {
		return result, err
	}

	files, err = store.AllObservedFiles(ctx, g.db)
	if err != nil {
		return result, err
	}
	result.Files = len(files)

	candidates, err := g.candidates(ctx, files)
	if err != nil {
		return result, err
	}

	decisions, err := loadDecisions(ctx, g.db)
	if err != nil {
		return result, err
	}

	clusters, refused, err := g.cluster(ctx, candidates, decisions)
	if err != nil {
		return result, err
	}
	result.HonouredApart = refused
	result.Groups = len(clusters)

	if g.beforeApply != nil {
		g.beforeApply()
	}

	moved, removed, err := g.apply(ctx, candidates, clusters, generation)
	if err != nil {
		return result, err
	}
	result.Moved = moved
	result.AssetsRemoved = removed

	return result, nil
}

// ensureHashes fills in perceptual hashes for thumbnails that do not have one.
func (g *Grouper) ensureHashes(ctx context.Context, files []store.ObservedFile) (int, error) {
	if g.thumbs == nil {
		return 0, nil
	}

	done := map[string]bool{}
	count := 0

	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		if f.LastHash == "" || done[f.LastHash] {
			continue
		}
		done[f.LastHash] = true

		existing, err := store.PreviewPHash(ctx, g.db, f.LastHash)
		if err != nil {
			return count, err
		}
		if existing != "" {
			continue
		}

		preview, err := store.PreviewByFileHash(ctx, g.db, f.LastHash)
		if err != nil {
			// No preview row at all, or a read error. Either way this file goes
			// through on name and time alone rather than dropping out.
			continue
		}
		if preview.Status != store.PreviewOK {
			continue
		}

		hash, err := g.hashThumbnail(f.LastHash)
		if err != nil {
			// A thumbnail that will not decode is a missing signal, not a
			// failure worth stopping for.
			continue
		}
		if err := store.SetPreviewPHash(ctx, g.db, f.LastHash, hash); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (g *Grouper) hashThumbnail(fileHash string) (string, error) {
	rc, err := g.thumbs.Open(fileHash)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	return computePHash(rc)
}

// candidates turns tracked files into what the scorer needs.
func (g *Grouper) candidates(ctx context.Context, files []store.ObservedFile) ([]candidate, error) {
	out := make([]candidate, 0, len(files))
	for _, f := range files {
		phash := ""
		if f.LastHash != "" {
			h, err := store.PreviewPHash(ctx, g.db, f.LastHash)
			if err != nil {
				return nil, err
			}
			phash = h
		}
		out = append(out, candidate{
			path:       f.Path,
			assetID:    f.AssetID,
			normalized: normalizeName(f.Path),
			modifiedAt: f.LastModifiedAt,
			phash:      phash,
		})
	}
	return out, nil
}

// decisionSet is the manual judgements, indexed for lookup in either order.
type decisionSet struct {
	together map[[2]string]bool
	apart    map[[2]string]bool
}

func loadDecisions(ctx context.Context, db *sql.DB) (decisionSet, error) {
	set := decisionSet{
		together: map[[2]string]bool{},
		apart:    map[[2]string]bool{},
	}

	all, err := store.AllGroupingDecisions(ctx, db)
	if err != nil {
		return set, err
	}
	for _, d := range all {
		key := [2]string{d.PathA, d.PathB}
		switch d.Decision {
		case store.Together:
			set.together[key] = true
		case store.Apart:
			set.apart[key] = true
		}
	}
	return set, nil
}

func pairKey(a, b string) [2]string {
	if a <= b {
		return [2]string{a, b}
	}
	return [2]string{b, a}
}

func (s decisionSet) isApart(a, b string) bool    { return s.apart[pairKey(a, b)] }
func (s decisionSet) isTogether(a, b string) bool { return s.together[pairKey(a, b)] }

// cluster groups candidates, returning the clusters and how many merges the
// user's decisions refused.
//
// Pairs are considered strongest first, so the most confident guesses claim
// their partners before weaker ones get a say.
func (g *Grouper) cluster(ctx context.Context, cands []candidate, decisions decisionSet) ([][]int, int, error) {
	uf := newUnionFind(len(cands))
	refused := 0

	// The user's "these are the same" comes first and unconditionally. It is a
	// decision, not a signal, so nothing outranks it — including an "apart"
	// that only applies transitively, where the user has contradicted
	// themselves and the more specific instruction wins.
	for i := range cands {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		for j := i + 1; j < len(cands); j++ {
			if decisions.isTogether(cands[i].path, cands[j].path) {
				uf.union(i, j)
			}
		}
	}

	type scored struct {
		i, j int
		s    float64
	}
	var pairs []scored
	for i := range cands {
		// Every pair is scored against every other, which on ten thousand
		// tracked files is fifty million comparisons and the better part of a
		// minute. Cancellation is checked once per row rather than per pair:
		// that is ten thousand cheap checks instead of fifty million, and it
		// still bounds the delay to one row's worth of work. Without it,
		// closing the window during a pass would appear to hang.
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		if g.sweepTick != nil {
			g.sweepTick(i)
		}
		for j := i + 1; j < len(cands); j++ {
			if decisions.isTogether(cands[i].path, cands[j].path) {
				continue // already joined
			}
			sc := scorePair(cands[i], cands[j], g.w)
			if sc.total >= g.w.Threshold {
				pairs = append(pairs, scored{i, j, sc.total})
			}
		}
	}
	sort.SliceStable(pairs, func(a, b int) bool { return pairs[a].s > pairs[b].s })

	for n, p := range pairs {
		// crossesApart walks both clusters, so this loop is not cheap either
		// once the groups get large.
		if n%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
		}
		if uf.find(p.i) == uf.find(p.j) {
			continue
		}
		// Merging two clusters is refused if any pair across them has been
		// pulled apart by hand. Without this check, transitivity would quietly
		// reunite files the user separated: A joins B, B joins C, and A and C
		// end up together however firmly the user said otherwise.
		if crossesApart(uf, cands, decisions, p.i, p.j) {
			refused++
			continue
		}
		uf.union(p.i, p.j)
	}

	return uf.clusters(), refused, nil
}

// crossesApart reports whether joining these two clusters would put a manually
// separated pair back together.
func crossesApart(uf *unionFind, cands []candidate, decisions decisionSet, i, j int) bool {
	left := uf.membersOf(i)
	right := uf.membersOf(j)
	for _, a := range left {
		for _, b := range right {
			if decisions.isApart(cands[a].path, cands[b].path) {
				return true
			}
		}
	}
	return false
}

// apply writes the clustering to the database, unless the catalogue has moved
// since generation was taken.
//
// The check is the whole point of the function's shape. Scoring happens outside
// any transaction because it takes far too long to hold one open, which leaves a
// window where the catalogue can change underneath a conclusion already drawn.
// A manual split landing in that window used to be undone by arithmetic older
// than the decision itself. Now the generation is re-read inside the
// transaction that would do the writing, so either nothing has changed and the
// writes are valid, or something has and nothing is written at all.
func (g *Grouper) apply(
	ctx context.Context,
	cands []candidate,
	clusters [][]int,
	generation int64,
) (moved, removed int, err error) {
	now := g.now()

	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("grouping: mulai transaksi: %w", err)
	}
	defer tx.Rollback()

	// Inside the transaction, before any write: read through the same handle
	// that will do the writing, so there is no gap left to lose.
	current, err := store.GroupingGeneration(ctx, tx)
	if err != nil {
		return 0, 0, err
	}
	if current != generation {
		return 0, 0, fmt.Errorf("%w (generasi %d -> %d)", ErrStale, generation, current)
	}

	touched := map[int64]bool{}

	for _, cluster := range clusters {
		// The lowest asset id is the canonical one: stable across runs, and in
		// practice the oldest, so a group keeps the identity it has had longest.
		canonical := cands[cluster[0]].assetID
		for _, idx := range cluster {
			if cands[idx].assetID < canonical {
				canonical = cands[idx].assetID
			}
		}

		for _, idx := range cluster {
			c := cands[idx]
			touched[c.assetID] = true
			if c.assetID == canonical {
				continue
			}
			n, err := store.MoveFileToAsset(ctx, tx, c.path, canonical, now)
			if err != nil {
				return 0, 0, err
			}
			// Count what actually moved. The generation check above means a
			// vanished path should be impossible here, but a counter that
			// reports moves it did not make is how a wrong number reaches the
			// user looking authoritative.
			moved += int(n)
		}
	}

	// Assets left holding nothing are removed. Anything still carrying a
	// version stays: deleting it would take part of the user's history with it,
	// and the database refuses that anyway.
	for assetID := range touched {
		gone, err := store.DeleteEmptyAsset(ctx, tx, assetID)
		if err != nil {
			return 0, 0, err
		}
		if gone {
			removed++
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("grouping: commit: %w", err)
	}
	return moved, removed, nil
}

// --- manual operations ------------------------------------------------------

// ErrUnknownPath is returned when an operation names a file that is not tracked.
var ErrUnknownPath = errors.New("grouping: jalur tidak dilacak")

// Split separates two files and records that decision permanently.
//
// The file named second moves to an asset of its own; anything else sharing the
// asset stays where it is. The recorded decision means no later scan will put
// these two back together, however alike they look.
func (g *Grouper) Split(ctx context.Context, keepPath, movePath string) error {
	if keepPath == movePath {
		return fmt.Errorf("grouping: butuh dua jalur berbeda")
	}
	now := g.now()

	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("grouping: mulai transaksi: %w", err)
	}
	defer tx.Rollback()

	keep, err := trackedFile(ctx, tx, keepPath)
	if err != nil {
		return err
	}
	move, err := trackedFile(ctx, tx, movePath)
	if err != nil {
		return err
	}

	if err := store.PutGroupingDecision(ctx, tx, keepPath, movePath, store.Apart, now); err != nil {
		return err
	}

	if keep.AssetID == move.AssetID {
		newAsset, err := store.CreateAsset(ctx, tx, filepath.Base(movePath), now)
		if err != nil {
			return err
		}
		if _, err := store.MoveFileToAsset(ctx, tx, movePath, newAsset, now); err != nil {
			return err
		}
		if _, err := store.DeleteEmptyAsset(ctx, tx, keep.AssetID); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("grouping: commit: %w", err)
	}
	return nil
}

// Merge joins two files into one asset and records that decision permanently.
func (g *Grouper) Merge(ctx context.Context, pathA, pathB string) error {
	if pathA == pathB {
		return fmt.Errorf("grouping: butuh dua jalur berbeda")
	}
	now := g.now()

	tx, err := g.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("grouping: mulai transaksi: %w", err)
	}
	defer tx.Rollback()

	a, err := trackedFile(ctx, tx, pathA)
	if err != nil {
		return err
	}
	b, err := trackedFile(ctx, tx, pathB)
	if err != nil {
		return err
	}

	if err := store.PutGroupingDecision(ctx, tx, pathA, pathB, store.Together, now); err != nil {
		return err
	}

	if a.AssetID != b.AssetID {
		canonical, absorbed := a.AssetID, b.AssetID
		movePath := pathB
		if absorbed < canonical {
			canonical, absorbed = absorbed, canonical
			movePath = pathA
		}
		if _, err := store.MoveFileToAsset(ctx, tx, movePath, canonical, now); err != nil {
			return err
		}
		if _, err := store.DeleteEmptyAsset(ctx, tx, absorbed); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("grouping: commit: %w", err)
	}
	return nil
}

func trackedFile(ctx context.Context, db store.DBTX, path string) (store.ObservedFile, error) {
	f, err := store.ObservedFileByPath(ctx, db, path)
	if errors.Is(err, store.ErrNotFound) {
		return store.ObservedFile{}, fmt.Errorf("%w: %s", ErrUnknownPath, path)
	}
	return f, err
}

// --- union-find -------------------------------------------------------------

type unionFind struct {
	parent  []int
	members [][]int
}

func newUnionFind(n int) *unionFind {
	uf := &unionFind{
		parent:  make([]int, n),
		members: make([][]int, n),
	}
	for i := range uf.parent {
		uf.parent[i] = i
		uf.members[i] = []int{i}
	}
	return uf
}

func (u *unionFind) find(i int) int {
	for u.parent[i] != i {
		u.parent[i] = u.parent[u.parent[i]]
		i = u.parent[i]
	}
	return i
}

func (u *unionFind) union(i, j int) {
	ri, rj := u.find(i), u.find(j)
	if ri == rj {
		return
	}
	// Keep the lower root so cluster order stays deterministic.
	if rj < ri {
		ri, rj = rj, ri
	}
	u.parent[rj] = ri
	u.members[ri] = append(u.members[ri], u.members[rj]...)
	u.members[rj] = nil
}

func (u *unionFind) membersOf(i int) []int {
	return u.members[u.find(i)]
}

// clusters returns the groups, each sorted, in a stable order.
func (u *unionFind) clusters() [][]int {
	var out [][]int
	for root, members := range u.members {
		if u.parent[root] != root || len(members) == 0 {
			continue
		}
		group := append([]int(nil), members...)
		sort.Ints(group)
		out = append(out, group)
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a][0] < out[b][0] })
	return out
}
