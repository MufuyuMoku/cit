package grouping

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/MufuyuMoku/cit/internal/store"
	"github.com/MufuyuMoku/cit/internal/ticket"
)

// Tickets hang off version rows, and grouping moves version rows between assets
// as a matter of routine — Detach, Split, Merge and a background Regroup all do
// it. These tests are the proof that a ticket cannot be severed, lost, or left on
// the wrong work by any of that.
//
// The import goes grouping's tests -> ticket. Neither package imports the other;
// the tests live here because the seam that lets a write land inside Regroup's
// thinking window is here.

// versionFor returns the newest version id recorded for a tracked path.
func (f *fixture) versionFor(path string) int64 {
	f.t.Helper()

	var id int64
	err := f.db.QueryRowContext(f.t.Context(), `
		SELECT id FROM versions WHERE file_key = ?
		ORDER BY observed_at DESC, id DESC LIMIT 1`, path).Scan(&id)
	if err != nil {
		f.t.Fatalf("cari versi untuk %s: %v", filepath.Base(path), err)
	}
	return id
}

func (f *fixture) tracker() *ticket.Tracker {
	return ticket.New(f.db, ticket.WithClock(func() time.Time { return baseTime }))
}

// A ticket on a file that is then pulled out of its work must still point at the
// same version, and must travel with it.
func TestTicketSurvivesDetach(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	tr := f.tracker()

	poster := []string{
		f.addFile("poster-kampus.psd", baseTime, artwork(3)),
		f.addFile("poster kampus final.psd", baseTime.Add(20*time.Minute), nearlyIdentical(3)),
		f.addFile("poster kampus fix.psd", baseTime.Add(40*time.Minute), artwork(3)),
	}
	f.regroup()
	originalAsset := f.requireSameAsset(poster...)

	odd := poster[2]
	versionID := f.versionFor(odd)

	tk, err := tr.Open(ctx, versionID, store.WaitingOnThem, "menunggu warna dari klien", "Bu Rina")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got, err := store.AssetIDForTicket(ctx, f.db, tk.ID); err != nil || got != originalAsset {
		t.Fatalf("karya tiket = %d (%v), mau %d", got, err, originalAsset)
	}

	if err := f.g.Detach(ctx, odd); err != nil {
		t.Fatalf("Detach: %v", err)
	}

	newAsset := f.assetOf(odd)
	if newAsset == originalAsset {
		t.Fatal("berkas tidak berpindah karya")
	}

	// Same version, same note, new work.
	got, err := store.TicketByID(ctx, f.db, tk.ID)
	if err != nil {
		t.Fatalf("tiket hilang setelah Detach: %v", err)
	}
	if got.VersionID != versionID {
		t.Errorf("tiket menunjuk versi %d, mau tetap %d", got.VersionID, versionID)
	}
	if got.Note != "menunggu warna dari klien" || got.Who != "Bu Rina" {
		t.Errorf("isi tiket berubah: %+v", got)
	}
	if got.Status != store.TicketOpen {
		t.Errorf("status tiket jadi %q; Detach tidak boleh mengubahnya", got.Status)
	}

	if held, err := store.AssetIDForTicket(ctx, f.db, tk.ID); err != nil {
		t.Fatalf("AssetIDForTicket: %v", err)
	} else if held != newAsset {
		t.Errorf("tiket ada di karya %d, mau ikut ke %d", held, newAsset)
	}

	onNew, err := store.TicketsForAsset(ctx, f.db, newAsset)
	if err != nil {
		t.Fatalf("TicketsForAsset: %v", err)
	}
	if len(onNew) != 1 || onNew[0].ID != tk.ID {
		t.Errorf("karya baru punya tiket %v, mau [%d]", onNew, tk.ID)
	}
	onOld, err := store.TicketsForAsset(ctx, f.db, originalAsset)
	if err != nil {
		t.Fatalf("TicketsForAsset: %v", err)
	}
	if len(onOld) != 0 {
		t.Errorf("%d tiket tertinggal di karya lama", len(onOld))
	}

	// The content is still protected from thinning, whichever work it sits on.
	hashes, err := store.FileHashesWithOpenTickets(ctx, f.db)
	if err != nil {
		t.Fatalf("FileHashesWithOpenTickets: %v", err)
	}
	version, err := store.VersionByID(ctx, f.db, versionID)
	if err != nil {
		t.Fatalf("VersionByID: %v", err)
	}
	if len(hashes) != 1 || hashes[0] != version.FileHash {
		t.Errorf("hash kebal pemangkasan = %v, mau [%s]", hashes, version.FileHash)
	}

	// Ten further passes must not drag it back or disturb the ticket.
	for i := 0; i < 10; i++ {
		f.regroup()
	}
	if f.assetOf(odd) != newAsset {
		t.Error("berkas tertarik kembali oleh pengelompokan otomatis")
	}
	if held, err := store.AssetIDForTicket(ctx, f.db, tk.ID); err != nil || held != newAsset {
		t.Errorf("tiket pindah karya setelah sepuluh pemindaian: %d (%v)", held, err)
	}
}

// Split moves the second file onto its own asset. The ticket on it must come too.
func TestTicketSurvivesSplit(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	tr := f.tracker()

	a := f.addFile("poster kampus.psd", baseTime, artwork(3))
	b := f.addFile("poster kampus fix.psd", baseTime.Add(30*time.Minute), nearlyIdentical(3))
	f.regroup()
	shared := f.requireSameAsset(a, b)

	versionA := f.versionFor(a)
	versionB := f.versionFor(b)

	keepTicket, err := tr.Open(ctx, versionA, store.WaitingOnMe, "kirim ke percetakan", "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	moveTicket, err := tr.Open(ctx, versionB, store.WaitingOnThem, "menunggu approval", "klien")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := f.g.Split(ctx, a, b); err != nil {
		t.Fatalf("Split: %v", err)
	}
	f.requireDifferentAssets(a, b)

	// Each ticket went with its own version, not with the asset it happened to
	// share.
	if held, err := store.AssetIDForTicket(ctx, f.db, keepTicket.ID); err != nil || held != f.assetOf(a) {
		t.Errorf("tiket yang tinggal ada di karya %d (%v), mau %d", held, err, f.assetOf(a))
	}
	if held, err := store.AssetIDForTicket(ctx, f.db, moveTicket.ID); err != nil || held != f.assetOf(b) {
		t.Errorf("tiket yang pindah ada di karya %d (%v), mau %d", held, err, f.assetOf(b))
	}

	for _, pair := range []struct {
		id      int64
		version int64
	}{{keepTicket.ID, versionA}, {moveTicket.ID, versionB}} {
		got, err := store.TicketByID(ctx, f.db, pair.id)
		if err != nil {
			t.Fatalf("TicketByID: %v", err)
		}
		if got.VersionID != pair.version {
			t.Errorf("tiket %d menunjuk versi %d, mau %d", pair.id, got.VersionID, pair.version)
		}
	}
	_ = shared
}

// The window between Regroup reading the catalogue and writing its conclusions is
// where M4a's generation counter earns its keep. A ticket opened inside that
// window must not be severed, and must end up on whatever work its version
// belongs to once the dust settles.
func TestTicketOpenedWhileRegroupIsInFlight(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	tr := f.tracker()

	a := f.addFile("poster kampus.psd", baseTime, artwork(3))
	b := f.addFile("poster kampus fix.psd", baseTime.Add(30*time.Minute), nearlyIdentical(3))
	f.requireDifferentAssets(a, b)

	versionB := f.versionFor(b)

	var tk store.Ticket
	var openErr error
	f.g.beforeApply = func() {
		tk, openErr = tr.Open(ctx, versionB, store.WaitingOnThem, "menunggu teks", "Bu Rina")
	}

	result, err := f.g.Regroup(ctx)
	if openErr != nil {
		t.Fatalf("buka tiket di tengah Regroup: %v", openErr)
	}
	if tk.ID == 0 {
		t.Fatal("celah antara penilaian dan penulisan tidak terlewati")
	}

	// Opening a ticket does not move the catalogue grouping reads — tickets are not
	// observed_files, versions or grouping_decisions — so the pass is still valid
	// and goes through. That is the right outcome: a ticket is not a reason to
	// throw away a regrouping.
	if err != nil {
		t.Fatalf("Regroup = %v", err)
	}
	if result.Moved != 1 {
		t.Errorf("pindah = %d, mau 1", result.Moved)
	}

	merged := f.requireSameAsset(a, b)

	// The ticket was not severed, still names the same version, and is on the work
	// that version now belongs to.
	got, err := store.TicketByID(ctx, f.db, tk.ID)
	if err != nil {
		t.Fatalf("tiket hilang: %v", err)
	}
	if got.VersionID != versionB {
		t.Errorf("tiket menunjuk versi %d, mau %d", got.VersionID, versionB)
	}
	if got.Status != store.TicketOpen {
		t.Errorf("status = %q; pengelompokan tidak boleh menyentuhnya", got.Status)
	}
	if held, err := store.AssetIDForTicket(ctx, f.db, tk.ID); err != nil || held != merged {
		t.Errorf("tiket ada di karya %d (%v), mau %d", held, err, merged)
	}
	f.requireVersionsFollow(a, b)
}

// The other half of the same window: a Detach landing inside it does move the
// catalogue, so the generation counter throws the pass away — and the ticket is
// untouched by the abort.
func TestDetachWithATicketDuringRegroupAbortsThePassAndKeepsTheTicket(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	tr := f.tracker()

	poster := []string{
		f.addFile("poster-kampus.psd", baseTime, artwork(3)),
		f.addFile("poster kampus final.psd", baseTime.Add(20*time.Minute), nearlyIdentical(3)),
	}
	f.regroup()
	f.requireSameAsset(poster...)

	odd := poster[1]
	versionID := f.versionFor(odd)
	tk, err := tr.Open(ctx, versionID, store.WaitingOnMe, "perbaiki margin", "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	var detachErr error
	f.g.beforeApply = func() {
		detachErr = f.g.Detach(ctx, odd)
		f.g.beforeApply = nil
	}

	_, err = f.g.Regroup(ctx)
	if detachErr != nil {
		t.Fatalf("Detach di tengah Regroup: %v", detachErr)
	}
	if !errors.Is(err, ErrStale) {
		t.Fatalf("Regroup = %v, mau ErrStale", err)
	}

	detached := f.assetOf(odd)
	got, err := store.TicketByID(ctx, f.db, tk.ID)
	if err != nil {
		t.Fatalf("tiket hilang: %v", err)
	}
	if got.VersionID != versionID {
		t.Errorf("tiket menunjuk versi %d, mau %d", got.VersionID, versionID)
	}
	if held, err := store.AssetIDForTicket(ctx, f.db, tk.ID); err != nil || held != detached {
		t.Errorf("tiket ada di karya %d (%v), mau ikut ke %d", held, err, detached)
	}
	if got.Status != store.TicketOpen {
		t.Errorf("status = %q", got.Status)
	}
}

// A work holding a ticketed version is not empty, so Regroup can never delete it
// out from under the ticket.
func TestRegroupNeverDeletesAnAssetHoldingATicketedVersion(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	tr := f.tracker()

	a := f.addFile("poster kampus.psd", baseTime, artwork(3))
	b := f.addFile("poster kampus fix.psd", baseTime.Add(30*time.Minute), nearlyIdentical(3))

	versionB := f.versionFor(b)
	tk, err := tr.Open(ctx, versionB, store.WaitingOnThem, "menunggu", "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Regrouping merges them, which empties one asset and removes it.
	result := f.regroup()
	if result.AssetsRemoved != 1 {
		t.Fatalf("karya dihapus = %d, mau 1", result.AssetsRemoved)
	}

	// The ticket's version moved rather than being deleted, so the ticket is fine
	// and still readable.
	if _, err := store.TicketByID(ctx, f.db, tk.ID); err != nil {
		t.Fatalf("tiket hilang saat karya kosong dihapus: %v", err)
	}
	held, err := store.AssetIDForTicket(ctx, f.db, tk.ID)
	if err != nil {
		t.Fatalf("AssetIDForTicket: %v", err)
	}
	if held != f.assetOf(a) || held != f.assetOf(b) {
		t.Errorf("tiket ada di karya %d, sementara berkasnya di %d dan %d",
			held, f.assetOf(a), f.assetOf(b))
	}
}
