package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- the constraint that defines the product -------------------------------

// A ticket that can float free turns CIT into a generic to-do application with a
// file browser attached. The schema must refuse one, not the code that happens to
// be writing today.
func TestATicketCannotStandOnItsOwn(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	// No version at all.
	_, err := AddTicket(ctx, db, Ticket{
		VersionID: 999999, Direction: WaitingOnThem,
		Note: "menunggu warna dari klien", CreatedAt: testTime,
	})
	if err == nil {
		t.Fatal("tiket yang menunjuk versi tidak ada diterima")
	}
	if !strings.Contains(err.Error(), "berdiri sendiri") {
		t.Errorf("pesan galat tidak menjelaskan aturannya: %v", err)
	}

	// Raw SQL, bypassing AddTicket entirely: the rule lives in the database.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO tickets (version_id, direction, status, note, created_at)
		VALUES (999999, 'waiting_on_them', 'open', 'lewat SQL mentah', ?)`,
		testTime.UnixNano()); err == nil {
		t.Error("INSERT mentah dengan versi tidak ada diterima")
	}

	// And version_id cannot be emptied afterwards either.
	assetID, ids := seedWork(t, db, "poster.psd", 1)
	id, err := AddTicket(ctx, db, Ticket{
		VersionID: ids[0], Direction: WaitingOnMe,
		Note: "kirim revisi", CreatedAt: testTime,
	})
	if err != nil {
		t.Fatalf("AddTicket: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE tickets SET version_id = NULL WHERE id = ?`, id); err == nil {
		t.Error("version_id bisa dikosongkan")
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE tickets SET version_id = 999999 WHERE id = ?`, id); err == nil {
		t.Error("version_id bisa diarahkan ke versi yang tidak ada")
	}

	// The asset underneath cannot be pulled away while it holds that version.
	if _, err := db.ExecContext(ctx, `DELETE FROM assets WHERE id = ?`, assetID); err == nil {
		t.Error("karya bisa dihapus padahal masih memegang versi bertiket")
	}
	// Nor can the version itself be deleted.
	if _, err := db.ExecContext(ctx, `DELETE FROM versions WHERE id = ?`, ids[0]); err == nil {
		t.Error("versi bertiket bisa dihapus")
	}
}

func TestTicketRejectsUnknownDirectionAndStatus(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()
	_, ids := seedWork(t, db, "poster.psd", 1)

	if _, err := AddTicket(ctx, db, Ticket{
		VersionID: ids[0], Direction: "entah", Note: "x", CreatedAt: testTime,
	}); err == nil {
		t.Error("arah tidak dikenal diterima")
	}

	// The database refuses it too, not just the helper.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO tickets (version_id, direction, status, note, created_at)
		VALUES (?, 'entah', 'open', 'x', ?)`, ids[0], testTime.UnixNano()); err == nil {
		t.Error("INSERT mentah dengan arah tidak dikenal diterima")
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO tickets (version_id, direction, status, note, created_at)
		VALUES (?, 'waiting_on_me', 'selesai-banget', 'x', ?)`,
		ids[0], testTime.UnixNano()); err == nil {
		t.Error("INSERT mentah dengan status tidak dikenal diterima")
	}

	id, err := AddTicket(ctx, db, Ticket{
		VersionID: ids[0], Direction: WaitingOnMe, Note: "x", CreatedAt: testTime,
	})
	if err != nil {
		t.Fatalf("AddTicket: %v", err)
	}
	if err := SetTicketStatus(ctx, db, id, "entah", testTime); err == nil {
		t.Error("status tidak dikenal diterima")
	}
	if err := SetTicketStatus(ctx, db, 999999, TicketClosed, testTime); !errors.Is(err, ErrNotFound) {
		t.Errorf("menutup tiket yang tidak ada = %v, mau ErrNotFound", err)
	}
}

// --- a newer version moves tickets to the inbox, and closes nothing ---------

// The rule is a trigger, so it holds for any future code that records a version
// without knowing tickets exist.
func TestANewerVersionFlagsOpenTicketsWithoutClosingThem(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	assetID, ids := seedWork(t, db, "poster.psd", 1)

	waiting, err := AddTicket(ctx, db, Ticket{
		VersionID: ids[0], Direction: WaitingOnThem,
		Note: "menunggu teks dari klien", Who: "Bu Rina", CreatedAt: testTime,
	})
	if err != nil {
		t.Fatalf("AddTicket: %v", err)
	}
	owed, err := AddTicket(ctx, db, Ticket{
		VersionID: ids[0], Direction: WaitingOnMe,
		Note: "rapikan tipografi", CreatedAt: testTime,
	})
	if err != nil {
		t.Fatalf("AddTicket: %v", err)
	}
	done, err := AddTicket(ctx, db, Ticket{
		VersionID: ids[0], Direction: WaitingOnMe,
		Note: "sudah beres sejak lama", CreatedAt: testTime,
	})
	if err != nil {
		t.Fatalf("AddTicket: %v", err)
	}
	if err := SetTicketStatus(ctx, db, done, TicketClosed, testTime); err != nil {
		t.Fatalf("tutup tiket: %v", err)
	}

	// A new save on the same work.
	saved := testTime.Add(3 * time.Hour)
	newVersion, err := AddVersion(ctx, db, Version{
		AssetID: assetID, FileHash: "hash-baru", Size: 2048,
		ObservedAt: saved, ModifiedAt: saved,
		SourcePath: "/kerja/poster.psd", FileKey: "/kerja/poster.psd",
	})
	if err != nil {
		t.Fatalf("AddVersion: %v", err)
	}

	for _, id := range []int64{waiting, owed} {
		got, err := TicketByID(ctx, db, id)
		if err != nil {
			t.Fatalf("TicketByID: %v", err)
		}
		if got.Status != TicketMaybeDone {
			t.Errorf("tiket %d berstatus %q, mau %q", id, got.Status, TicketMaybeDone)
		}
		if !got.FlaggedAt.Equal(saved) {
			t.Errorf("tiket %d ditandai pada %v, mau %v", id, got.FlaggedAt, saved)
		}
		if !got.ClosedAt.IsZero() {
			t.Errorf("tiket %d ikut ditutup; versi baru tidak boleh menutup apa pun", id)
		}
		if !got.IsOpen() {
			t.Errorf("tiket %d dianggap tidak terbuka lagi padahal belum ditinjau", id)
		}
	}

	// Already closed stays closed; it is not dragged back into the inbox.
	if got, err := TicketByID(ctx, db, done); err != nil {
		t.Fatalf("TicketByID: %v", err)
	} else if got.Status != TicketClosed {
		t.Errorf("tiket yang sudah ditutup berubah jadi %q", got.Status)
	}

	// And a ticket opened against the brand-new version is not flagged by its own
	// arrival.
	fresh, err := AddTicket(ctx, db, Ticket{
		VersionID: newVersion, Direction: WaitingOnThem,
		Note: "minta persetujuan", CreatedAt: saved,
	})
	if err != nil {
		t.Fatalf("AddTicket: %v", err)
	}
	if got, err := TicketByID(ctx, db, fresh); err != nil {
		t.Fatalf("TicketByID: %v", err)
	} else if got.Status != TicketOpen {
		t.Errorf("tiket baru langsung berstatus %q", got.Status)
	}
}

// A save on a different work must leave this work's tickets alone.
func TestANewVersionOnAnotherWorkFlagsNothing(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	_, posterIDs := seedWork(t, db, "poster.psd", 1)
	logoAsset, _ := seedWork(t, db, "logo.png", 1)

	id, err := AddTicket(ctx, db, Ticket{
		VersionID: posterIDs[0], Direction: WaitingOnThem,
		Note: "menunggu warna", CreatedAt: testTime,
	})
	if err != nil {
		t.Fatalf("AddTicket: %v", err)
	}

	at := testTime.Add(time.Hour)
	if _, err := AddVersion(ctx, db, Version{
		AssetID: logoAsset, FileHash: "logo-baru", Size: 10,
		ObservedAt: at, ModifiedAt: at,
		SourcePath: "/kerja/logo.png", FileKey: "/kerja/logo.png",
	}); err != nil {
		t.Fatalf("AddVersion: %v", err)
	}

	got, err := TicketByID(ctx, db, id)
	if err != nil {
		t.Fatalf("TicketByID: %v", err)
	}
	if got.Status != TicketOpen {
		t.Errorf("tiket poster jadi %q karena logo disimpan", got.Status)
	}
}

// --- the query M7 will need -------------------------------------------------

// "A version with an open ticket is immune from thinning" has no exceptions, and
// thinning works by content hash. This is the question retention will ask.
func TestFileHashesWithOpenTicketsCoversEverythingNotClosed(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	assetID, ids := seedWork(t, db, "poster.psd", 3)
	oldest, err := VersionByID(ctx, db, ids[0])
	if err != nil {
		t.Fatalf("VersionByID: %v", err)
	}
	middle, err := VersionByID(ctx, db, ids[1])
	if err != nil {
		t.Fatalf("VersionByID: %v", err)
	}

	if got, err := FileHashesWithOpenTickets(ctx, db); err != nil || len(got) != 0 {
		t.Fatalf("tanpa tiket: %v (%v), mau kosong", got, err)
	}

	openID, err := AddTicket(ctx, db, Ticket{
		VersionID: oldest.ID, Direction: WaitingOnThem,
		Note: "menunggu klien", CreatedAt: testTime,
	})
	if err != nil {
		t.Fatalf("AddTicket: %v", err)
	}
	flaggedID, err := AddTicket(ctx, db, Ticket{
		VersionID: middle.ID, Direction: WaitingOnMe,
		Note: "perbaiki margin", CreatedAt: testTime,
	})
	if err != nil {
		t.Fatalf("AddTicket: %v", err)
	}
	if err := SetTicketStatus(ctx, db, flaggedID, TicketMaybeDone, testTime); err != nil {
		t.Fatalf("tandai: %v", err)
	}

	got, err := FileHashesWithOpenTickets(ctx, db)
	if err != nil {
		t.Fatalf("FileHashesWithOpenTickets: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("%d hash, mau 2 (open dan maybe_done): %v", len(got), got)
	}
	if !contains(got, oldest.FileHash) || !contains(got, middle.FileHash) {
		t.Errorf("hash = %v, mau memuat %q dan %q", got, oldest.FileHash, middle.FileHash)
	}

	// maybe_done counts: the user has not finished with it, and throwing the bytes
	// away would answer their question by destroying the evidence.
	if !contains(got, middle.FileHash) {
		t.Error("tiket di kotak tinjauan tidak melindungi isinya")
	}

	// Closing one releases only that one.
	if err := SetTicketStatus(ctx, db, openID, TicketClosed, testTime); err != nil {
		t.Fatalf("tutup: %v", err)
	}
	got, err = FileHashesWithOpenTickets(ctx, db)
	if err != nil {
		t.Fatalf("FileHashesWithOpenTickets: %v", err)
	}
	if len(got) != 1 || got[0] != middle.FileHash {
		t.Errorf("setelah satu ditutup: %v, mau hanya %q", got, middle.FileHash)
	}

	if held, err := VersionHasOpenTicket(ctx, db, middle.ID); err != nil || !held {
		t.Errorf("VersionHasOpenTicket(middle) = %v (%v), mau true", held, err)
	}
	if held, err := VersionHasOpenTicket(ctx, db, oldest.ID); err != nil || held {
		t.Errorf("VersionHasOpenTicket(oldest) = %v (%v), mau false", held, err)
	}

	if n, err := CountOpenTickets(ctx, db); err != nil || n != 1 {
		t.Errorf("CountOpenTickets = %d (%v), mau 1", n, err)
	}
	_ = assetID
}

// --- tickets follow their version, not a stored asset id -------------------

func TestTicketsForAssetFollowsTheVersion(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	fromAsset, ids := seedWork(t, db, "poster.psd", 1)
	toAsset, _ := seedWork(t, db, "tujuan.psd", 1)

	id, err := AddTicket(ctx, db, Ticket{
		VersionID: ids[0], Direction: WaitingOnThem,
		Note: "menunggu brief", CreatedAt: testTime,
	})
	if err != nil {
		t.Fatalf("AddTicket: %v", err)
	}

	if got, err := AssetIDForTicket(ctx, db, id); err != nil || got != fromAsset {
		t.Fatalf("karya tiket = %d (%v), mau %d", got, err, fromAsset)
	}

	// Move the version the way grouping does. The path has to be the one seedWork
	// actually wrote, separators and all.
	posterPath := filepath.Join("/kerja", "poster.psd")
	moved, err := MoveFileToAsset(ctx, db, posterPath, toAsset, testTime)
	if err != nil {
		t.Fatalf("MoveFileToAsset: %v", err)
	}
	if moved != 1 {
		t.Fatalf("MoveFileToAsset memindahkan %d jalur, mau 1", moved)
	}

	if got, err := AssetIDForTicket(ctx, db, id); err != nil || got != toAsset {
		t.Errorf("karya tiket setelah dipindah = %d (%v), mau %d", got, err, toAsset)
	}

	onOld, err := TicketsForAsset(ctx, db, fromAsset)
	if err != nil {
		t.Fatalf("TicketsForAsset: %v", err)
	}
	if len(onOld) != 0 {
		t.Errorf("%d tiket masih di karya lama", len(onOld))
	}
	onNew, err := TicketsForAsset(ctx, db, toAsset)
	if err != nil {
		t.Fatalf("TicketsForAsset: %v", err)
	}
	if len(onNew) != 1 || onNew[0].ID != id {
		t.Fatalf("tiket di karya baru = %v, mau satu tiket %d", onNew, id)
	}

	// And it is still the same version, with the same note.
	if onNew[0].VersionID != ids[0] {
		t.Errorf("tiket menunjuk versi %d, mau tetap %d", onNew[0].VersionID, ids[0])
	}
	if onNew[0].Note != "menunggu brief" {
		t.Errorf("catatan tiket berubah jadi %q", onNew[0].Note)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
