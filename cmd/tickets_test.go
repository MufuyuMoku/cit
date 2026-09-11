package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MufuyuMoku/cit/internal/store"
)

func TestAgeText(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "baru saja"},
		{40 * time.Minute, "baru saja"},
		{5 * time.Hour, "5 jam"},
		{23 * time.Hour, "23 jam"},
		{25 * time.Hour, "sehari"},
		{6 * 24 * time.Hour, "6 hari"},
		{40 * 24 * time.Hour, "40 hari"},
	}
	for _, tc := range cases {
		if got := ageText(tc.d); got != tc.want {
			t.Errorf("ageText(%v) = %q, mau %q", tc.d, got, tc.want)
		}
	}
}

// The whole ticket surface the interface talks to, over a real assembled service:
// open in both directions, see them in their own lists, have a newer save move one
// into the review inbox, and close it.
func TestTicketBindingsOverARealService(t *testing.T) {
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
	defer svc.shutdown()

	app := &App{ctx: context.Background(), svc: svc}
	ctx := app.ctx

	// A work with one save, placed the way ingest would leave it.
	path := filepath.Join(work, "poster kampus.png")
	writeTestPNG(t, path)
	hash, err := svc.vault.Store(ctx, mustOpen(t, path))
	if err != nil {
		t.Fatalf("vault.Store: %v", err)
	}
	assetID, err := store.CreateAsset(ctx, svc.db, "poster kampus.png", time.Now())
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	versionID, err := store.AddVersion(ctx, svc.db, store.Version{
		AssetID: assetID, FileHash: hash, Size: 100,
		ObservedAt: time.Now(), ModifiedAt: time.Now(),
		SourcePath: path, FileKey: path,
	})
	if err != nil {
		t.Fatalf("AddVersion: %v", err)
	}
	if _, err := svc.previews.Generate(ctx, hash, path); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// A note is required: a ticket with nothing written on it cannot be acted on
	// and cannot be dismissed with confidence.
	if _, err := app.OpenTicket(versionID, "waiting_on_them", "", "klien"); err == nil {
		t.Error("tiket tanpa catatan diterima")
	}
	if _, err := app.OpenTicket(versionID, "entah", "menunggu", ""); err == nil {
		t.Error("arah tidak dikenal diterima")
	}

	theirs, err := app.OpenTicket(versionID, "waiting_on_them", "menunggu teks dari klien", "Bu Rina")
	if err != nil {
		t.Fatalf("OpenTicket: %v", err)
	}
	if theirs.AssetID != assetID {
		t.Errorf("tiket mengaku milik karya %d, mau %d", theirs.AssetID, assetID)
	}
	if theirs.ThumbURL == "" {
		t.Error("tiket tidak membawa gambar kecil versinya")
	}
	if theirs.Status != "open" {
		t.Errorf("status = %q", theirs.Status)
	}

	if _, err := app.OpenTicket(versionID, "waiting_on_me", "rapikan tipografi", ""); err != nil {
		t.Fatalf("OpenTicket: %v", err)
	}

	// Two directions, two lists, no mixing.
	onThem, err := app.WaitingOnThem()
	if err != nil {
		t.Fatalf("WaitingOnThem: %v", err)
	}
	if len(onThem) != 1 || onThem[0].Direction != "waiting_on_them" {
		t.Errorf("daftar menunggu orang lain = %v", onThem)
	}
	onMe, err := app.WaitingOnMe()
	if err != nil {
		t.Fatalf("WaitingOnMe: %v", err)
	}
	if len(onMe) != 1 || onMe[0].Direction != "waiting_on_me" {
		t.Errorf("daftar utang pekerjaan = %v", onMe)
	}

	if inbox, err := app.ReviewInbox(); err != nil || len(inbox) != 0 {
		t.Fatalf("kotak tinjauan = %v (%v), mau kosong", inbox, err)
	}

	status := app.Status()
	if status.OpenTickets != 2 {
		t.Errorf("status.OpenTickets = %d, mau 2", status.OpenTickets)
	}
	if status.ToReview != 0 {
		t.Errorf("status.ToReview = %d, mau 0", status.ToReview)
	}

	// A newer save lands on the same work.
	later := time.Now().Add(time.Hour)
	if _, err := store.AddVersion(ctx, svc.db, store.Version{
		AssetID: assetID, FileHash: "hash-berikutnya", Size: 200,
		ObservedAt: later, ModifiedAt: later,
		SourcePath: path, FileKey: path,
	}); err != nil {
		t.Fatalf("AddVersion: %v", err)
	}

	inbox, err := app.ReviewInbox()
	if err != nil {
		t.Fatalf("ReviewInbox: %v", err)
	}
	if len(inbox) != 2 {
		t.Fatalf("%d tiket di kotak tinjauan, mau 2", len(inbox))
	}
	for _, v := range inbox {
		if v.Status != "maybe_done" {
			t.Errorf("tiket %d berstatus %q, mau maybe_done", v.ID, v.Status)
		}
		if v.ClosedAt != "" {
			t.Errorf("tiket %d ikut ditutup oleh versi baru", v.ID)
		}
		if v.FlaggedAt == "" {
			t.Errorf("tiket %d tidak mencatat kapan ditandai", v.ID)
		}
	}

	// Still outstanding, so retention may not touch the content underneath.
	if status := app.Status(); status.OpenTickets != 2 || status.ToReview != 2 {
		t.Errorf("status setelah ditandai = terbuka %d, tinjau %d; mau 2 dan 2",
			status.OpenTickets, status.ToReview)
	}
	held, err := store.FileHashesWithOpenTickets(ctx, svc.db)
	if err != nil {
		t.Fatalf("FileHashesWithOpenTickets: %v", err)
	}
	if len(held) != 1 || held[0] != hash {
		t.Errorf("hash kebal pemangkasan = %v, mau [%s]", held, hash)
	}

	// The user decides.
	if err := app.CloseTicket(theirs.ID); err != nil {
		t.Fatalf("CloseTicket: %v", err)
	}
	if inbox, err := app.ReviewInbox(); err != nil || len(inbox) != 1 {
		t.Errorf("kotak tinjauan setelah satu ditutup = %v (%v), mau satu", inbox, err)
	}

	// Reopening puts it back where it can be worked on.
	if err := app.ReopenTicket(theirs.ID); err != nil {
		t.Fatalf("ReopenTicket: %v", err)
	}
	onThem, err = app.WaitingOnThem()
	if err != nil {
		t.Fatalf("WaitingOnThem: %v", err)
	}
	if len(onThem) != 1 || onThem[0].ID != theirs.ID {
		t.Errorf("tiket tidak kembali ke daftarnya: %v", onThem)
	}

	// The work's own page shows everything, closed included.
	all, err := app.TicketsForAsset(assetID)
	if err != nil {
		t.Fatalf("TicketsForAsset: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("%d tiket di halaman karya, mau 2", len(all))
	}
}
