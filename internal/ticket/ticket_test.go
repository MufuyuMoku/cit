package ticket

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/MufuyuMoku/cit/internal/store"
)

var baseTime = time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)

type fixture struct {
	t     *testing.T
	db    *sql.DB
	tr    *Tracker
	clock time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	db, err := store.Open(filepath.Join(t.TempDir(), "cit.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close: %v", err)
		}
	})

	f := &fixture{t: t, db: db, clock: baseTime}
	f.tr = New(db, WithClock(func() time.Time { return f.clock }))
	return f
}

// work creates an asset with the given number of saves and returns the asset id
// plus the version ids, oldest first.
func (f *fixture) work(name string, saves int) (int64, []int64) {
	f.t.Helper()
	ctx := f.t.Context()

	path := filepath.Join("kerja", name)
	assetID, err := store.CreateAsset(ctx, f.db, name, baseTime)
	if err != nil {
		f.t.Fatalf("CreateAsset: %v", err)
	}

	var ids []int64
	var hash string
	for i := 0; i < saves; i++ {
		at := baseTime.Add(time.Duration(i) * time.Hour)
		hash = name + "-h" + string(rune('a'+i))
		id, err := store.AddVersion(ctx, f.db, store.Version{
			AssetID: assetID, FileHash: hash, Size: int64(1000 + i),
			ObservedAt: at, ModifiedAt: at, SourcePath: path, FileKey: path,
		})
		if err != nil {
			f.t.Fatalf("AddVersion: %v", err)
		}
		ids = append(ids, id)
	}
	if err := store.PutObservedFile(ctx, f.db, store.ObservedFile{
		Path: path, AssetID: assetID, LastHash: hash, LastSize: 1000,
		LastModifiedAt: baseTime, LastSeenAt: baseTime, LastVerifiedAt: baseTime,
	}); err != nil {
		f.t.Fatalf("PutObservedFile: %v", err)
	}
	return assetID, ids
}

func TestOpenRequiresAVersionAndANote(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	_, ids := f.work("poster.psd", 1)

	if _, err := f.tr.Open(ctx, ids[0], store.WaitingOnThem, "", "klien"); !errors.Is(err, ErrNoNote) {
		t.Errorf("tiket tanpa catatan = %v, mau ErrNoNote", err)
	}
	if _, err := f.tr.Open(ctx, 999999, store.WaitingOnThem, "menunggu", ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("tiket untuk versi tak ada = %v, mau ErrNotFound", err)
	}
	if _, err := f.tr.Open(ctx, ids[0], "entah", "menunggu", ""); err == nil {
		t.Error("arah tidak dikenal diterima")
	}

	got, err := f.tr.Open(ctx, ids[0], store.WaitingOnThem, "menunggu teks", "Bu Rina")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got.Status != store.TicketOpen {
		t.Errorf("status awal %q", got.Status)
	}
	if got.Who != "Bu Rina" || got.Note != "menunggu teks" {
		t.Errorf("tiket = %+v", got)
	}
	if !got.CreatedAt.Equal(baseTime) {
		t.Errorf("dibuat pada %v, mau %v", got.CreatedAt, baseTime)
	}
}

// The two directions are different questions, and the lists must not mix them.
// "Waiting on them" carries its age because that is the fact that makes someone
// follow a thing up; work owed does not need a number, only to be listed.
func TestTheTwoDirectionsAreSeparateListsAndAgeIsShown(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	_, ids := f.work("poster.psd", 1)

	// Opened six days ago.
	f.clock = baseTime.Add(-6 * 24 * time.Hour)
	theirs, err := f.tr.Open(ctx, ids[0], store.WaitingOnThem, "menunggu warna dari klien", "Bu Rina")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	f.clock = baseTime.Add(-2 * time.Hour)
	if _, err := f.tr.Open(ctx, ids[0], store.WaitingOnMe, "rapikan tipografi", ""); err != nil {
		t.Fatalf("Open: %v", err)
	}

	f.clock = baseTime

	waitingOnThem, err := f.tr.Waiting(ctx, store.WaitingOnThem)
	if err != nil {
		t.Fatalf("Waiting: %v", err)
	}
	if len(waitingOnThem) != 1 {
		t.Fatalf("%d tiket menunggu orang lain, mau 1", len(waitingOnThem))
	}
	if waitingOnThem[0].ID != theirs.ID {
		t.Errorf("tiket yang salah di daftar menunggu orang lain")
	}
	if days := int(waitingOnThem[0].Age.Hours() / 24); days != 6 {
		t.Errorf("umur = %v (%d hari), mau 6 hari", waitingOnThem[0].Age, days)
	}
	if waitingOnThem[0].AssetName != "poster.psd" {
		t.Errorf("nama karya = %q", waitingOnThem[0].AssetName)
	}
	if waitingOnThem[0].FileName != "poster.psd" {
		t.Errorf("nama berkas = %q", waitingOnThem[0].FileName)
	}

	waitingOnMe, err := f.tr.Waiting(ctx, store.WaitingOnMe)
	if err != nil {
		t.Fatalf("Waiting: %v", err)
	}
	if len(waitingOnMe) != 1 {
		t.Fatalf("%d tiket utang pekerjaan, mau 1", len(waitingOnMe))
	}
	if waitingOnMe[0].Direction != store.WaitingOnMe {
		t.Errorf("arah = %q", waitingOnMe[0].Direction)
	}
}

// Longest wait first. Burying the thing that has been waiting a week under
// today's arrivals is how a queue stops being useful.
func TestWaitingListsTheLongestWaitFirst(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	_, ids := f.work("poster.psd", 1)

	for _, d := range []time.Duration{2 * time.Hour, 9 * 24 * time.Hour, 3 * 24 * time.Hour} {
		f.clock = baseTime.Add(-d)
		if _, err := f.tr.Open(ctx, ids[0], store.WaitingOnThem, "menunggu "+d.String(), ""); err != nil {
			t.Fatalf("Open: %v", err)
		}
	}
	f.clock = baseTime

	list, err := f.tr.Waiting(ctx, store.WaitingOnThem)
	if err != nil {
		t.Fatalf("Waiting: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("%d tiket, mau 3", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].Age < list[i].Age {
			t.Errorf("urutan salah: umur %v sebelum %v", list[i-1].Age, list[i].Age)
		}
	}
}

// A newer version moves tickets into the inbox and closes nothing. Only a person
// closes a ticket.
func TestReviewInboxCollectsWhatANewerVersionSuggests(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	assetID, ids := f.work("poster.psd", 1)

	waiting, err := f.tr.Open(ctx, ids[0], store.WaitingOnThem, "menunggu teks", "Bu Rina")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if inbox, err := f.tr.ReviewInbox(ctx); err != nil || len(inbox) != 0 {
		t.Fatalf("kotak tinjauan awal = %v (%v), mau kosong", inbox, err)
	}

	// A new save lands.
	saved := baseTime.Add(4 * time.Hour)
	if _, err := store.AddVersion(ctx, f.db, store.Version{
		AssetID: assetID, FileHash: "poster-baru", Size: 3000,
		ObservedAt: saved, ModifiedAt: saved,
		SourcePath: filepath.Join("kerja", "poster.psd"),
		FileKey:    filepath.Join("kerja", "poster.psd"),
	}); err != nil {
		t.Fatalf("AddVersion: %v", err)
	}

	inbox, err := f.tr.ReviewInbox(ctx)
	if err != nil {
		t.Fatalf("ReviewInbox: %v", err)
	}
	if len(inbox) != 1 || inbox[0].ID != waiting.ID {
		t.Fatalf("kotak tinjauan = %v, mau tiket %d", inbox, waiting.ID)
	}
	if inbox[0].Status != store.TicketMaybeDone {
		t.Errorf("status = %q, mau %q", inbox[0].Status, store.TicketMaybeDone)
	}
	if !inbox[0].IsOpen() {
		t.Error("tiket di kotak tinjauan dianggap sudah tidak terbuka")
	}

	// It leaves the direction lists: it is waiting on the user's judgement now,
	// not on the world.
	if list, err := f.tr.Waiting(ctx, store.WaitingOnThem); err != nil || len(list) != 0 {
		t.Errorf("masih di daftar menunggu orang lain: %v (%v)", list, err)
	}

	// The user decides. Closing empties the inbox.
	f.clock = baseTime.Add(5 * time.Hour)
	if err := f.tr.Close(ctx, waiting.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if inbox, err := f.tr.ReviewInbox(ctx); err != nil || len(inbox) != 0 {
		t.Errorf("kotak tinjauan setelah ditutup = %v (%v)", inbox, err)
	}
	got, err := store.TicketByID(ctx, f.db, waiting.ID)
	if err != nil {
		t.Fatalf("TicketByID: %v", err)
	}
	if got.IsOpen() {
		t.Error("tiket yang ditutup masih terbuka")
	}
	if !got.ClosedAt.Equal(f.clock) {
		t.Errorf("ditutup pada %v, mau %v", got.ClosedAt, f.clock)
	}
}

// It was not done after all. Reopening puts it back in the direction list.
func TestReopenPutsATicketBackInFlight(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	assetID, ids := f.work("poster.psd", 1)

	tk, err := f.tr.Open(ctx, ids[0], store.WaitingOnMe, "perbaiki margin", "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	saved := baseTime.Add(time.Hour)
	if _, err := store.AddVersion(ctx, f.db, store.Version{
		AssetID: assetID, FileHash: "poster-baru", Size: 1,
		ObservedAt: saved, ModifiedAt: saved,
		SourcePath: filepath.Join("kerja", "poster.psd"),
		FileKey:    filepath.Join("kerja", "poster.psd"),
	}); err != nil {
		t.Fatalf("AddVersion: %v", err)
	}

	if err := f.tr.Reopen(ctx, tk.ID); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if inbox, err := f.tr.ReviewInbox(ctx); err != nil || len(inbox) != 0 {
		t.Errorf("masih di kotak tinjauan: %v (%v)", inbox, err)
	}
	list, err := f.tr.Waiting(ctx, store.WaitingOnMe)
	if err != nil {
		t.Fatalf("Waiting: %v", err)
	}
	if len(list) != 1 || list[0].ID != tk.ID {
		t.Errorf("daftar utang = %v, mau tiket %d", list, tk.ID)
	}

	// Reopening a closed one works the same way.
	if err := f.tr.Close(ctx, tk.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := f.tr.Reopen(ctx, tk.ID); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	got, err := store.TicketByID(ctx, f.db, tk.ID)
	if err != nil {
		t.Fatalf("TicketByID: %v", err)
	}
	if got.Status != store.TicketOpen {
		t.Errorf("status = %q", got.Status)
	}
	if !got.ClosedAt.IsZero() {
		t.Errorf("waktu penutupan masih %v setelah dibuka lagi", got.ClosedAt)
	}
}

func TestForAssetShowsClosedTicketsToo(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	assetID, ids := f.work("poster.psd", 2)

	open, err := f.tr.Open(ctx, ids[1], store.WaitingOnMe, "masih dikerjakan", "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	closed, err := f.tr.Open(ctx, ids[0], store.WaitingOnThem, "sudah dijawab", "klien")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := f.tr.Close(ctx, closed.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}

	list, err := f.tr.ForAsset(ctx, assetID)
	if err != nil {
		t.Fatalf("ForAsset: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("%d tiket, mau 2: halaman karya harus menunjukkan yang sudah beres juga", len(list))
	}

	seen := map[int64]store.TicketStatus{}
	for _, v := range list {
		seen[v.ID] = v.Status
		if v.AssetID != assetID {
			t.Errorf("tiket %d mengaku milik karya %d", v.ID, v.AssetID)
		}
	}
	if seen[open.ID] != store.TicketOpen || seen[closed.ID] != store.TicketClosed {
		t.Errorf("status = %v", seen)
	}
}

func TestViewCarriesTheVersionHashNotAPath(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	assetID, ids := f.work("poster.psd", 1)

	if _, err := f.tr.Open(ctx, ids[0], store.WaitingOnThem, "menunggu", ""); err != nil {
		t.Fatalf("Open: %v", err)
	}

	list, err := f.tr.ForAsset(ctx, assetID)
	if err != nil {
		t.Fatalf("ForAsset: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("%d tiket", len(list))
	}
	version, err := store.VersionByID(ctx, f.db, ids[0])
	if err != nil {
		t.Fatalf("VersionByID: %v", err)
	}
	if list[0].FileHash != version.FileHash {
		t.Errorf("hash = %q, mau %q", list[0].FileHash, version.FileHash)
	}
	if !list[0].VersionObservedAt.Equal(version.ObservedAt) {
		t.Errorf("waktu versi = %v, mau %v", list[0].VersionObservedAt, version.ObservedAt)
	}
}
