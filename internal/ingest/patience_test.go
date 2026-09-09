package ingest

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/MufuyuMoku/cit/internal/store"
)

// --- B: size-scaled quiet period, the only protection on Linux and macOS ----

// On Unix there is no mandatory locking, so checkOpenable can never say "a
// writer still has this". Everything rests on the settling rules, and a flat
// three seconds is not enough for a file that takes minutes to write.
//
// This test deliberately does not rely on the file being held open: the writer
// has closed it, so the Windows lock check reports it free on every platform.
// What holds it back is the size-scaled patience and nothing else.
func TestSizeScaledQuietPeriodHoldsBackLargeFilesWithNoLock(t *testing.T) {
	// 100 KiB of patience per second: a 2 MiB file must hold still for 20s.
	f := newFixture(t, WithQuietScale(100<<10), WithMaxQuietPeriod(time.Minute))

	const size = 2 << 20
	f.write("ekspor.mp4", size, 1)

	// The writer has closed the file. Nothing is locked.
	state, err := f.ing.checkOpenable(f.path("ekspor.mp4"))
	if err != nil || state != openFree {
		t.Fatalf("prasyarat gagal: berkas seharusnya bebas, dapat %v (%v)", state, err)
	}

	f.scan()

	// Past the flat quiet period, nowhere near the scaled one.
	f.clock.Advance(2 * testQuietPeriod)
	if res := f.scan(); res.NewVersions != 0 {
		t.Fatalf("berkas besar diproses setelah %v; masa tenang berskala seharusnya menahannya: %+v",
			2*testQuietPeriod, res)
	}

	f.clock.Advance(10 * time.Second) // total 16s, still under 20s
	if res := f.scan(); res.NewVersions != 0 {
		t.Fatalf("berkas besar diproses sebelum masa tenang berskala habis: %+v", res)
	}

	// Now past it.
	f.clock.Advance(10 * time.Second)
	res := f.scan()
	if res.NewVersions != 1 {
		t.Fatalf("setelah masa tenang berskala habis: NewVersions = %d, mau 1: %+v",
			res.NewVersions, res)
	}
	f.requireRestores(f.versionsOf(f.assetIDFor("ekspor.mp4"))[0], f.path("ekspor.mp4"))
}

func TestQuietForScalesWithSizeAndIsCapped(t *testing.T) {
	i := New(nil, nil,
		WithQuietPeriod(3*time.Second),
		WithQuietScale(10<<20),
		WithMaxQuietPeriod(time.Minute))

	cases := []struct {
		size int64
		want time.Duration
	}{
		{1 << 10, 3 * time.Second},    // tiny: the floor applies
		{10 << 20, 3 * time.Second},   // 1s scaled, still under the floor
		{100 << 20, 10 * time.Second}, // 10s scaled
		{600 << 20, 60 * time.Second}, // 60s scaled, exactly the cap
		{100 << 30, 60 * time.Second}, // enormous: capped
	}
	for _, c := range cases {
		if got := i.quietFor(c.size); got != c.want {
			t.Errorf("quietFor(%d) = %v, mau %v", c.size, got, c.want)
		}
	}

	flat := New(nil, nil, WithQuietPeriod(3*time.Second), WithQuietScale(0))
	if got := flat.quietFor(100 << 30); got != 3*time.Second {
		t.Errorf("penskalaan mati: quietFor = %v, mau 3s", got)
	}
}

// --- A: a locked file waits, everything else gets a way out -----------------

// A locked file is a writer mid-save. Waiting is right, and there is no retry
// limit: a video export legitimately holds its output for an hour.
func TestLockedFileWaitsWithoutLimit(t *testing.T) {
	f := newFixture(t)
	f.write("ekspor.mp4", 256<<10, 1)

	locked := true
	f.ing.checkOpenable = func(string) (openState, error) {
		if locked {
			return openLocked, nil
		}
		return openFree, nil
	}

	// Far more scans than the retry limit for unreadable files. A locked file
	// must not be given up on.
	for i := 0; i < maxOpenAttempts*4; i++ {
		f.clock.Advance(testQuietPeriod)
		res := f.scan()
		if res.NewVersions != 0 {
			t.Fatalf("putaran %d: berkas terkunci diproses: %+v", i, res)
		}
		if res.Unreadable != 0 {
			t.Fatalf("putaran %d: berkas terkunci dianggap tidak bisa dibaca: %+v", i, res)
		}
	}
	if got := len(f.ing.Problems()); got != 0 {
		t.Errorf("%d masalah dicatat untuk berkas yang sekadar terkunci", got)
	}

	// The writer finishes.
	locked = false
	f.clock.Advance(testQuietPeriod)
	if res := f.scan(); res.NewVersions != 1 {
		t.Fatalf("setelah kunci dilepas: NewVersions = %d, mau 1: %+v", res.NewVersions, res)
	}
}

// A file that will not open for some other reason — a disconnected network
// drive, an unreadable permission — must not wait forever in silence. That
// would trade one invisible failure for another.
var errDriveGone = errors.New("drive jaringan tidak tersedia")

func TestUnreadableFileGivesUpAndIsReported(t *testing.T) {
	f := newFixture(t)
	f.write("di-drive-jaringan.psd", 256<<10, 1)

	// Count real attempts rather than scans: a scan where the file has not
	// settled yet never reaches the open at all.
	attempts := 0
	f.ing.checkOpenable = func(string) (openState, error) {
		attempts++
		return openUnknown, errDriveGone
	}

	var res Result
	for scan := 0; scan < 4*maxOpenAttempts && res.Unreadable == 0; scan++ {
		f.clock.Advance(testQuietPeriod)
		res = f.scan()

		switch {
		case res.Unreadable == 0 && attempts >= maxOpenAttempts:
			t.Fatalf("sudah %d percobaan tanpa dilaporkan; berkas menunggu selamanya tanpa jejak",
				attempts)
		case res.Unreadable != 0 && attempts < maxOpenAttempts:
			t.Fatalf("menyerah setelah %d percobaan, mau bertahan sampai %d",
				attempts, maxOpenAttempts)
		}
	}
	if res.Unreadable != 1 {
		t.Fatalf("Unreadable = %d, mau 1 setelah %d percobaan: %+v", res.Unreadable, attempts, res)
	}

	problems := f.ing.Problems()
	if len(problems) != 1 {
		t.Fatalf("%d masalah dicatat, mau 1", len(problems))
	}
	p := problems[0]
	if p.Path != f.path("di-drive-jaringan.psd") {
		t.Errorf("jalur masalah = %q", p.Path)
	}
	if !errors.Is(p.Err, errDriveGone) {
		t.Errorf("galat masalah = %v, mau errDriveGone", p.Err)
	}
	if p.Attempts < maxOpenAttempts {
		t.Errorf("percobaan = %d, mau minimal %d", p.Attempts, maxOpenAttempts)
	}

	// And it stops spinning on it.
	for i := 0; i < 3; i++ {
		f.clock.Advance(testQuietPeriod)
		if res := f.scan(); res.Settled != 0 {
			t.Errorf("masih mencoba membuka berkas yang sudah dilaporkan: %+v", res)
		}
	}

	// The drive comes back and the file changes: the problem clears and the
	// file is ingested normally.
	f.ing.checkOpenable = func(string) (openState, error) { return openFree, nil }
	f.write("di-drive-jaringan.psd", 300<<10, 2)
	f.clock.Advance(testQuietPeriod)
	f.scan()
	f.clock.Advance(testQuietPeriod)
	if res := f.scan(); res.NewVersions != 1 {
		t.Fatalf("setelah drive kembali: NewVersions = %d, mau 1: %+v", res.NewVersions, res)
	}
	if got := len(f.ing.Problems()); got != 0 {
		t.Errorf("%d masalah tersisa setelah berkas berhasil dibaca", got)
	}
}

// --- verification: mtime and size can lie -----------------------------------

// Some applications restore the original mtime after saving. If the size
// happens to match too, the cheap signals say nothing changed and a new version
// would be lost with no error and no trace — the worst failure this program
// has. Periodic re-reading turns "never" into "within a known window".
func TestContentChangeHiddenBySameSizeAndMtimeIsCaughtByVerification(t *testing.T) {
	const verifyEvery = 30 * time.Minute
	f := newFixture(t, WithVerifyInterval(verifyEvery))

	p := f.write("licik.psd", 400<<10, 1)
	f.settle()
	if got := f.versionCount(); got != 1 {
		t.Fatalf("versi awal = %d, mau 1", got)
	}

	before, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Rewrite the content, then put the size and mtime back exactly.
	if err := os.WriteFile(p, payload(400<<10, 99), 0o600); err != nil {
		t.Fatalf("tulis ulang: %v", err)
	}
	if err := os.Chtimes(p, before.ModTime(), before.ModTime()); err != nil {
		t.Fatalf("kembalikan mtime: %v", err)
	}
	after, _ := os.Stat(p)
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("prasyarat gagal: ukuran/mtime tidak berhasil dikembalikan persis")
	}

	// The cheap signals see nothing. This is the silent-loss window, and it is
	// real: nothing detects the change here.
	for i := 0; i < 5; i++ {
		f.clock.Advance(testQuietPeriod)
		if res := f.scan(); res.NewVersions != 0 {
			t.Fatalf("terdeteksi lebih awal dari yang diharapkan: %+v", res)
		}
	}
	if got := f.versionCount(); got != 1 {
		t.Fatalf("versi = %d sebelum verifikasi jatuh tempo, mau 1", got)
	}

	// Verification falls due and the change surfaces.
	f.clock.Advance(verifyEvery)
	res := f.scan()
	if res.NewVersions != 1 {
		t.Fatalf("verifikasi berkala gagal menemukan perubahan: %+v", res)
	}
	if got := f.versionCount(); got != 2 {
		t.Errorf("versi = %d setelah verifikasi, mau 2", got)
	}

	versions := f.versionsOf(f.assetIDFor("licik.psd"))
	f.requireRestores(versions[1], p)
}

// When verification finds the content really is unchanged, it records that the
// check happened and creates no version.
func TestVerificationOfUnchangedFileRecordsTheCheckWithoutAVersion(t *testing.T) {
	const verifyEvery = 30 * time.Minute
	f := newFixture(t, WithVerifyInterval(verifyEvery))

	f.write("tenang.psd", 300<<10, 1)
	f.settle()

	tracked := f.tracked("tenang.psd")
	firstVerified := tracked.LastVerifiedAt
	if firstVerified.IsZero() {
		t.Fatal("last_verified_at kosong setelah versi pertama")
	}

	f.clock.Advance(verifyEvery)
	res := f.scan()
	if res.Verified != 1 {
		t.Errorf("Verified = %d, mau 1: %+v", res.Verified, res)
	}
	if res.NewVersions != 0 {
		t.Errorf("NewVersions = %d, mau 0; isinya memang tidak berubah", res.NewVersions)
	}
	if got := f.versionCount(); got != 1 {
		t.Errorf("versi = %d, mau 1", got)
	}

	after := f.tracked("tenang.psd")
	if !after.LastVerifiedAt.After(firstVerified) {
		t.Errorf("last_verified_at tidak maju: %v -> %v", firstVerified, after.LastVerifiedAt)
	}
}

// Verification off means the old behaviour: cheap signals only.
func TestVerificationCanBeDisabled(t *testing.T) {
	f := newFixture(t, WithVerifyInterval(0))

	f.write("tenang.psd", 200<<10, 1)
	f.settle()

	f.clock.Advance(24 * time.Hour)
	if res := f.scan(); res.Verified != 0 {
		t.Errorf("Verified = %d dengan verifikasi dimatikan: %+v", res.Verified, res)
	}
}

// --- last_verified_at is meant to be shown to the user ----------------------

func TestVerificationTimestampIsReadableForDisplay(t *testing.T) {
	f := newFixture(t)

	f.write("a.psd", 200<<10, 1)
	f.write("b.psd", 200<<10, 2)
	f.settle()

	assetID := f.assetIDFor("a.psd")

	byAsset, err := store.ObservedFilesByAsset(t.Context(), f.db, assetID)
	if err != nil {
		t.Fatalf("ObservedFilesByAsset: %v", err)
	}
	if len(byAsset) != 1 {
		t.Fatalf("%d jalur untuk karya, mau 1", len(byAsset))
	}
	if byAsset[0].LastVerifiedAt.IsZero() {
		t.Error("last_verified_at kosong; pengguna tidak bisa tahu kapan berkas benar-benar diperiksa")
	}

	// The scheduler's view: which files have not been checked lately.
	stale, err := store.ObservedFilesVerifiedBefore(t.Context(), f.db, f.clock.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("ObservedFilesVerifiedBefore: %v", err)
	}
	if len(stale) != 2 {
		t.Errorf("%d jalur belum diverifikasi sebelum batas, mau 2", len(stale))
	}

	fresh, err := store.ObservedFilesVerifiedBefore(t.Context(), f.db, f.clock.Now().Add(-time.Hour), 10)
	if err != nil {
		t.Fatalf("ObservedFilesVerifiedBefore: %v", err)
	}
	if len(fresh) != 0 {
		t.Errorf("%d jalur dianggap basi padahal baru diverifikasi", len(fresh))
	}
}
