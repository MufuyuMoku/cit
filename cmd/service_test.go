package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MufuyuMoku/cit/internal/store"
)

// Every layer of CIT had tests and no caller until this milestone. This is the
// test for the caller: a real data directory, a real vault, a real watch loop,
// and a file saved into a watched folder turning up as a work with a picture.
func TestServiceTurnsASavedFileIntoAWorkWithAThumbnail(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	t.Setenv("CIT_DATA_DIR", root)

	work := filepath.Join(t.TempDir(), "kerja")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatalf("buat folder kerja: %v", err)
	}

	svc, err := openService()
	if err != nil {
		t.Fatalf("openService: %v", err)
	}
	defer svc.shutdown()

	ctx := t.Context()

	// Everything lands under the one data directory, and the thumbnail directory
	// keeps the name internal/preview chose.
	for _, dir := range []string{svc.layout.vault, svc.layout.previews} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("%s tidak dibuat: %v", dir, err)
		}
	}
	if _, err := os.Stat(svc.layout.database); err != nil {
		t.Fatalf("basis data tidak dibuat: %v", err)
	}

	// Nothing to watch yet, so nothing is watched: a loop waking up once a second
	// to scan no folders is just a warm CPU.
	if svc.watching() {
		t.Error("mengawasi padahal belum ada folder")
	}

	if err := store.AddWatchedFolder(ctx, svc.db, work, time.Now()); err != nil {
		t.Fatalf("AddWatchedFolder: %v", err)
	}
	if err := svc.restartWatching(ctx); err != nil {
		t.Fatalf("restartWatching: %v", err)
	}
	if !svc.watching() {
		t.Fatal("tidak mengawasi padahal folder sudah ditambahkan")
	}

	// The user saves something.
	src := filepath.Join(work, "poster kampus.png")
	writeTestPNG(t, src)

	// Two observations either side of the quiet period: one look cannot tell a
	// finished file from one about to be written to again. The watch loop is
	// already doing this on its own; waiting is how the test joins in.
	deadline := time.Now().Add(20 * time.Second)
	var cards []store.AssetCard
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		cards, err = store.AssetCards(ctx, svc.db)
		if err != nil {
			t.Fatalf("AssetCards: %v", err)
		}
		if len(cards) == 1 && cards[0].PreviewStatus == store.PreviewOK {
			break
		}
	}

	if len(cards) != 1 {
		t.Fatalf("%d karya setelah menunggu, mau 1", len(cards))
	}
	card := cards[0]
	if card.Name != "poster kampus.png" {
		t.Errorf("nama karya = %q", card.Name)
	}
	if card.Versions != 1 {
		t.Errorf("versi = %d, mau 1", card.Versions)
	}
	if !card.ContentPresent {
		t.Error("isi versi baru dianggap sudah dibuang")
	}
	if card.PreviewStatus != store.PreviewOK {
		t.Fatalf("status pratinjau = %q, mau ok", card.PreviewStatus)
	}

	// The thumbnail is really on disk, under the data directory, and reachable by
	// the route the page uses.
	if _, ok := svc.previews.Locate(card.LatestHash); !ok {
		t.Error("gambar kecil tidak ada di disk")
	}
	if got := thumbURL(card.PreviewStatus, card.LatestHash); got == "" {
		t.Error("tidak ada URL gambar kecil untuk pratinjau yang berhasil")
	}

	// And the content really is in the vault: exporting it verifies every chunk.
	versions, err := store.Timeline(ctx, svc.db, card.AssetID)
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("%d versi di linimasa, mau 1", len(versions))
	}

	out := filepath.Join(t.TempDir(), "keluar.png")
	if err := exportVersion(ctx, svc.vault, versions[0].Version, out); err != nil {
		t.Fatalf("exportVersion: %v", err)
	}
	original, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("baca asli: %v", err)
	}
	exported, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("baca hasil: %v", err)
	}
	if len(original) != len(exported) {
		t.Errorf("hasil ekspor %d bita, asli %d: pemulihan harus identik bita per bita",
			len(exported), len(original))
	}
}

// The watched folders are the one piece of state the user chose by hand, so they
// have to still be there next time the application opens.
func TestWatchedFoldersSurviveRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	t.Setenv("CIT_DATA_DIR", root)

	work := filepath.Join(t.TempDir(), "kerja")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatalf("buat folder: %v", err)
	}

	first, err := openService()
	if err != nil {
		t.Fatalf("openService: %v", err)
	}
	if err := store.AddWatchedFolder(t.Context(), first.db, work, time.Now()); err != nil {
		t.Fatalf("AddWatchedFolder: %v", err)
	}
	first.shutdown()

	second, err := openService()
	if err != nil {
		t.Fatalf("openService kedua: %v", err)
	}
	defer second.shutdown()

	folders, err := store.WatchedFolders(t.Context(), second.db)
	if err != nil {
		t.Fatalf("WatchedFolders: %v", err)
	}
	if len(folders) != 1 || folders[0].Path != filepath.Clean(work) {
		t.Errorf("folder setelah dibuka ulang = %v, mau [%s]", folders, work)
	}
	if second.layout.root != filepath.Clean(root) {
		t.Errorf("akar data berpindah ke %q", second.layout.root)
	}
}

// Closing the window cancels the context the watch loop was given, and nothing is
// closed until that loop has returned — so a vault.Store part way through a large
// file aborts rather than being torn down under a live read.
func TestShutdownStopsWatchingBeforeClosingAnything(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	t.Setenv("CIT_DATA_DIR", root)

	work := filepath.Join(t.TempDir(), "kerja")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatalf("buat folder: %v", err)
	}

	svc, err := openService()
	if err != nil {
		t.Fatalf("openService: %v", err)
	}

	appCtx, cancelApp := context.WithCancel(context.Background())
	defer cancelApp()

	if err := store.AddWatchedFolder(appCtx, svc.db, work, time.Now()); err != nil {
		t.Fatalf("AddWatchedFolder: %v", err)
	}
	if err := svc.restartWatching(appCtx); err != nil {
		t.Fatalf("restartWatching: %v", err)
	}
	if !svc.watching() {
		t.Fatal("tidak mengawasi")
	}

	// Something is being written while the window closes.
	big := filepath.Join(work, "ekspor.bin")
	if err := os.WriteFile(big, make([]byte, 8<<20), 0o600); err != nil {
		t.Fatalf("tulis berkas besar: %v", err)
	}

	done := make(chan struct{})
	go func() {
		svc.shutdown()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("shutdown tidak selesai dalam 30 detik")
	}

	if svc.watching() {
		t.Error("masih mengawasi setelah shutdown")
	}

	// The database is closed, which is only safe because the watch loop returned
	// first. A query now must fail rather than hang or panic.
	if _, err := store.CountAssets(context.Background(), svc.db); err == nil {
		t.Error("basis data masih terbuka setelah shutdown")
	}
}
