package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/MufuyuMoku/cit/internal/grouping"
	"github.com/MufuyuMoku/cit/internal/store"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is what the interface talks to.
//
// Every method here is a question the user asked by clicking something, so every
// one of them reports its own failure as a returned error for the page to show
// quietly. Nothing in CIT interrupts.
type App struct {
	ctx context.Context
	svc *service
}

// NewApp returns an unstarted App.
func NewApp() *App { return &App{} }

// startup opens everything and begins watching.
//
// The context kept here is the one Wails cancels on shutdown, and it is the same
// one handed to the watch loop — so closing the window while a 200 MB file is
// being ingested cancels that vault.Store rather than finishing a read nobody is
// waiting for.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	svc, err := openService()
	if err != nil {
		// Nothing can work without this, and there is no interface yet to show it
		// in. The window will open and say so through Status.
		log.Printf("cit: tidak bisa membuka penyimpanan: %v", err)
		return
	}
	a.svc = svc

	if err := svc.restartWatching(ctx); err != nil {
		log.Printf("cit: tidak bisa mulai mengawasi: %v", err)
	}
}

// shutdown is called by Wails as the window closes.
func (a *App) shutdown(ctx context.Context) {
	if a.svc != nil {
		a.svc.shutdown()
	}
}

// --- status -----------------------------------------------------------------

// Status is what the interface needs to describe the state of things without
// asking three separate questions.
type Status struct {
	Ready    bool     `json:"ready"`
	Problem  string   `json:"problem"`
	DataDir  string   `json:"dataDir"`
	Folders  []string `json:"folders"`
	Watching bool     `json:"watching"`
	Assets   int      `json:"assets"`
	Versions int      `json:"versions"`

	// OpenTickets is everything not yet closed; ToReview is the subset sitting in
	// the review inbox. Both are plain counts shown in place — there is no badge
	// and nothing turns red.
	OpenTickets int      `json:"openTickets"`
	ToReview    int      `json:"toReview"`
	Unreadable  []string `json:"unreadable"`
	AppVersion  string   `json:"appVersion"`
}

// Status reports the state of the application.
func (a *App) Status() Status {
	s := Status{AppVersion: buildInfo().Version}
	if a.svc == nil {
		s.Problem = "Penyimpanan CIT tidak bisa dibuka. Periksa catatan aplikasi."
		return s
	}
	s.Ready = true
	s.DataDir = a.svc.layout.root
	s.Watching = a.svc.watching()
	s.Problem = a.svc.watchError()

	folders, err := store.WatchedFolders(a.ctx, a.svc.db)
	if err != nil {
		s.Problem = err.Error()
		return s
	}
	for _, f := range folders {
		s.Folders = append(s.Folders, f.Path)
	}

	if n, err := store.CountAssets(a.ctx, a.svc.db); err == nil {
		s.Assets = n
	}
	if n, err := store.CountVersions(a.ctx, a.svc.db); err == nil {
		s.Versions = n
	}
	if n, err := store.CountOpenTickets(a.ctx, a.svc.db); err == nil {
		s.OpenTickets = n
	}
	if list, err := store.TicketsByStatus(a.ctx, a.svc.db, store.TicketMaybeDone); err == nil {
		s.ToReview = len(list)
	}
	for _, p := range a.svc.ingester.Problems() {
		s.Unreadable = append(s.Unreadable,
			fmt.Sprintf("%s — %v", p.Path, p.Err))
	}
	return s
}

// --- watched folders --------------------------------------------------------

// ChooseFolder asks the user for a folder and starts watching it.
func (a *App) ChooseFolder() (string, error) {
	if a.svc == nil {
		return "", errNotReady
	}
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Pilih folder kerja untuk diawasi",
	})
	if err != nil {
		return "", err
	}
	if dir == "" {
		return "", nil // cancelled
	}
	if err := store.AddWatchedFolder(a.ctx, a.svc.db, dir, time.Now()); err != nil {
		return "", err
	}
	if err := a.svc.restartWatching(a.ctx); err != nil {
		return "", err
	}
	return filepath.Clean(dir), nil
}

// RemoveFolder stops watching a folder. Nothing already recorded is removed.
func (a *App) RemoveFolder(path string) error {
	if a.svc == nil {
		return errNotReady
	}
	if err := store.RemoveWatchedFolder(a.ctx, a.svc.db, path); err != nil {
		return err
	}
	return a.svc.restartWatching(a.ctx)
}

// --- the overview -----------------------------------------------------------

// AssetCardView is one work on the main page.
type AssetCardView struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	UpdatedAt string `json:"updatedAt"`
	Versions  int    `json:"versions"`
	Files     int    `json:"files"`

	// ThumbURL is empty when there is no picture to show, which is a normal
	// outcome rather than a fault: an unrecognised format, or a video on a
	// machine with no ffmpeg.
	ThumbURL string `json:"thumbUrl"`

	// AlphaFlattened means the thumbnail lost its transparency to fit the size
	// budget. It has to be marked: a white logo on transparency composited onto
	// white is an empty rectangle, and an empty rectangle reads as "this version
	// was blank", which is a lie about the user's own work.
	AlphaFlattened bool `json:"alphaFlattened"`

	// ContentPresent is false when retention has discarded the bytes behind the
	// newest version.
	ContentPresent bool `json:"contentPresent"`
}

// ListAssets returns every work, most recently touched first.
func (a *App) ListAssets() ([]AssetCardView, error) {
	if a.svc == nil {
		return nil, errNotReady
	}
	cards, err := store.AssetCards(a.ctx, a.svc.db)
	if err != nil {
		return nil, err
	}

	out := make([]AssetCardView, 0, len(cards))
	for _, c := range cards {
		out = append(out, AssetCardView{
			ID:             c.AssetID,
			Name:           c.Name,
			UpdatedAt:      c.UpdatedAt.Format(time.RFC3339),
			Versions:       c.Versions,
			Files:          c.Files,
			ThumbURL:       thumbURL(c.PreviewStatus, c.LatestHash),
			AlphaFlattened: c.AlphaFlattened,
			ContentPresent: c.ContentPresent,
		})
	}
	return out, nil
}

// --- one work ---------------------------------------------------------------

// VersionView is one entry on a timeline.
type VersionView struct {
	ID         int64  `json:"id"`
	ObservedAt string `json:"observedAt"`
	ModifiedAt string `json:"modifiedAt"`
	Size       int64  `json:"size"`
	FileName   string `json:"fileName"`
	FileKey    string `json:"fileKey"`
	Hash       string `json:"hash"`

	ThumbURL       string `json:"thumbUrl"`
	AlphaFlattened bool   `json:"alphaFlattened"`

	// PreviewNote explains an absent picture, so a blank square is never left
	// unexplained.
	PreviewNote string `json:"previewNote"`

	// ContentPresent false means retention has discarded the bytes. The row stays
	// on the timeline regardless — it may never have a hole in it — and
	// ContentReleasedAt says when the content went.
	ContentPresent    bool   `json:"contentPresent"`
	ContentReleasedAt string `json:"contentReleasedAt"`

	Pinned bool `json:"pinned"`
}

// TrackedFileView is one file on disk that belongs to this work.
type TrackedFileView struct {
	Path string `json:"path"`

	// LastVerifiedAt is when the content was last actually read and hashed, as
	// opposed to assumed unchanged from its size and modification time. Shown
	// because those are different promises and CIT does not blur them.
	LastVerifiedAt string `json:"lastVerifiedAt"`
	LastSeenAt     string `json:"lastSeenAt"`
	Exists         bool   `json:"exists"`
}

// AssetDetail is one work's page.
type AssetDetail struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	CreatedAt string            `json:"createdAt"`
	Versions  []VersionView     `json:"versions"`
	Files     []TrackedFileView `json:"files"`
}

// GetAsset returns one work with its whole timeline.
func (a *App) GetAsset(id int64) (AssetDetail, error) {
	if a.svc == nil {
		return AssetDetail{}, errNotReady
	}

	asset, err := store.AssetByID(a.ctx, a.svc.db, id)
	if err != nil {
		return AssetDetail{}, err
	}
	entries, err := store.Timeline(a.ctx, a.svc.db, id)
	if err != nil {
		return AssetDetail{}, err
	}
	files, err := store.ObservedFilesByAsset(a.ctx, a.svc.db, id)
	if err != nil {
		return AssetDetail{}, err
	}

	detail := AssetDetail{
		ID:        asset.ID,
		Name:      asset.Name,
		CreatedAt: asset.CreatedAt.Format(time.RFC3339),
	}

	for _, e := range entries {
		v := VersionView{
			ID:             e.Version.ID,
			ObservedAt:     e.Version.ObservedAt.Format(time.RFC3339),
			ModifiedAt:     e.Version.ModifiedAt.Format(time.RFC3339),
			Size:           e.Version.Size,
			FileName:       filepath.Base(e.Version.FileKey),
			FileKey:        e.Version.FileKey,
			Hash:           shortHash(e.Version.FileHash),
			ThumbURL:       thumbURL(e.PreviewStatus, e.Version.FileHash),
			AlphaFlattened: e.AlphaFlattened,
			PreviewNote:    previewNote(e),
			ContentPresent: e.Version.ContentPresent,
			Pinned:         e.Version.Pinned,
		}
		if !e.Version.ContentReleasedAt.IsZero() {
			v.ContentReleasedAt = e.Version.ContentReleasedAt.Format(time.RFC3339)
		}
		detail.Versions = append(detail.Versions, v)
	}

	for _, f := range files {
		view := TrackedFileView{
			Path:       f.Path,
			LastSeenAt: f.LastSeenAt.Format(time.RFC3339),
		}
		// The epoch means "never actually verified", which is what migration 2
		// left existing rows as. Showing 1970 would be worse than showing
		// nothing.
		if f.LastVerifiedAt.Unix() > 0 {
			view.LastVerifiedAt = f.LastVerifiedAt.Format(time.RFC3339)
		}
		if _, err := os.Stat(f.Path); err == nil {
			view.Exists = true
		}
		detail.Files = append(detail.Files, view)
	}
	sort.Slice(detail.Files, func(i, j int) bool {
		return detail.Files[i].Path < detail.Files[j].Path
	})

	return detail, nil
}

// previewNote explains why a version has no picture, in the user's language.
func previewNote(e store.TimelineEntry) string {
	switch e.PreviewStatus {
	case store.PreviewOK:
		if e.AlphaFlattened {
			return "Transparansi diratakan ke putih agar gambar kecilnya masuk anggaran ukuran."
		}
		return ""
	case store.PreviewUnsupported:
		return "Format ini belum punya gambar kecil. Versinya tetap tersimpan utuh."
	case store.PreviewFailed:
		if e.PreviewErr != "" {
			return "Gambar kecil gagal dibuat: " + e.PreviewErr
		}
		return "Gambar kecil gagal dibuat. Versinya tetap tersimpan utuh."
	default:
		return "Gambar kecil belum dibuat."
	}
}

// --- actions ----------------------------------------------------------------

// OpenVersion opens a version in the application that owns its file type.
//
// The returned string is a note to show when what was opened is a read-only copy
// rather than the file itself; empty when the real file was opened.
func (a *App) OpenVersion(versionID int64) (string, error) {
	if a.svc == nil {
		return "", errNotReady
	}
	version, err := store.VersionByID(a.ctx, a.svc.db, versionID)
	if err != nil {
		return "", err
	}
	return a.svc.openVersion(a.ctx, version)
}

// ExportVersion asks the user where to put a copy of a version and writes it
// there, verified.
//
// Returns the path written, or "" if the user cancelled.
func (a *App) ExportVersion(versionID int64) (string, error) {
	if a.svc == nil {
		return "", errNotReady
	}
	version, err := store.VersionByID(a.ctx, a.svc.db, versionID)
	if err != nil {
		return "", err
	}
	if !version.ContentPresent {
		return "", errors.New("isi versi ini sudah tidak disimpan lagi, jadi tidak ada yang bisa diekspor")
	}

	dest, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Simpan salinan versi ini",
		DefaultFilename: suggestedExportName(version),
	})
	if err != nil {
		return "", err
	}
	if dest == "" {
		return "", nil
	}
	if err := exportVersion(a.ctx, a.svc.vault, version, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// PinVersion marks a version immune from thinning, or lifts that mark.
func (a *App) PinVersion(versionID int64, pinned bool) error {
	if a.svc == nil {
		return errNotReady
	}
	return store.SetVersionPinned(a.ctx, a.svc.db, versionID, pinned)
}

// DetachFile separates one tracked file from the work it is currently grouped
// with, permanently.
//
// Grouping is applied without asking because it can be undone; this is the
// undoing. The decision is recorded against every file the path was sharing an
// asset with, so no later scan can quietly pull it back.
func (a *App) DetachFile(path string) error {
	if a.svc == nil {
		return errNotReady
	}
	if err := a.svc.grouper.Detach(a.ctx, path); err != nil {
		if errors.Is(err, grouping.ErrUnknownPath) {
			return fmt.Errorf("berkas ini tidak lagi dilacak, jadi tidak ada yang bisa dipisahkan")
		}
		return err
	}
	return nil
}

// ExplainGrouping reports why two tracked files did or did not end up as one
// work. It changes nothing.
func (a *App) ExplainGrouping(pathA, pathB string) (string, error) {
	if a.svc == nil {
		return "", errNotReady
	}
	e, err := a.svc.grouper.Explain(a.ctx, pathA, pathB)
	if err != nil {
		return "", err
	}
	return e.String(), nil
}

// RevealDataDir opens the folder CIT keeps its data in.
func (a *App) RevealDataDir() error {
	if a.svc == nil {
		return errNotReady
	}
	return openInDefaultApp(a.svc.layout.root)
}

var errNotReady = errors.New("penyimpanan CIT belum siap")

// GetBuildInfo reports the version of the running application.
func (a *App) GetBuildInfo() BuildInfo { return buildInfo() }
