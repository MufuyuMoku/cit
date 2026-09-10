package grouping

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// scheduled wires a Scheduler to a fixture with a clock the test drives.
type scheduled struct {
	*fixture
	s     *Scheduler
	clock time.Time

	mu      sync.Mutex
	results []Result
	errs    []error
}

func newScheduled(t *testing.T, minInterval time.Duration) *scheduled {
	t.Helper()

	sc := &scheduled{fixture: newFixture(t), clock: baseTime}
	sc.s = NewScheduler(sc.g,
		WithMinInterval(minInterval),
		WithSchedulerClock(func() time.Time {
			sc.mu.Lock()
			defer sc.mu.Unlock()
			return sc.clock
		}),
		WithOnDone(func(r Result, err error) {
			sc.mu.Lock()
			defer sc.mu.Unlock()
			sc.results = append(sc.results, r)
			sc.errs = append(sc.errs, err)
		}),
	)
	return sc
}

func (sc *scheduled) advance(d time.Duration) {
	sc.mu.Lock()
	sc.clock = sc.clock.Add(d)
	sc.mu.Unlock()
}

// notice delivers a scan outcome and waits for any pass it starts.
func (sc *scheduled) notice(changed bool) {
	sc.t.Helper()

	sc.s.Notice(sc.t.Context(), changed)
	if err := sc.s.Wait(sc.t.Context()); err != nil {
		sc.t.Fatalf("Wait: %v", err)
	}
}

func (sc *scheduled) passes() int {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return len(sc.results)
}

func (sc *scheduled) lastErr() error {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if len(sc.errs) == 0 {
		return nil
	}
	return sc.errs[len(sc.errs)-1]
}

// A scan that recorded something means the user is still working. Grouping waits
// for them to stop rather than competing with the next save.
func TestSchedulerWaitsForAQuietScan(t *testing.T) {
	sc := newScheduled(t, 0)
	a := sc.addFile("poster kampus.psd", baseTime, artwork(3))
	b := sc.addFile("poster kampus fix.psd", baseTime.Add(30*time.Minute), nearlyIdentical(3))

	for i := 0; i < 5; i++ {
		sc.notice(true)
	}
	if sc.passes() != 0 {
		t.Errorf("%d pengelompokan jalan saat pengguna masih menyimpan; mau 0", sc.passes())
	}
	if !sc.s.Pending() {
		t.Error("perubahan tidak tercatat sebagai pekerjaan tertunda")
	}
	sc.requireDifferentAssets(a, b)

	// They stopped.
	sc.notice(false)
	if sc.passes() != 1 {
		t.Fatalf("%d pengelompokan setelah pemindaian tenang; mau 1", sc.passes())
	}
	if err := sc.lastErr(); err != nil {
		t.Fatalf("pengelompokan: %v", err)
	}
	sc.requireSameAsset(a, b)
	if sc.s.Pending() {
		t.Error("masih tertunda setelah pengelompokan berhasil")
	}
}

// Nothing to do means nothing runs, however many quiet scans arrive.
func TestSchedulerDoesNothingWhenNothingChanged(t *testing.T) {
	sc := newScheduled(t, 0)
	sc.addFile("poster kampus.psd", baseTime, artwork(3))

	for i := 0; i < 5; i++ {
		sc.notice(false)
	}
	if sc.passes() != 0 {
		t.Errorf("%d pengelompokan tanpa ada perubahan; mau 0", sc.passes())
	}
}

// The floor stops a long working session from spending its time regrouping: every
// pause between saves reads as quiet, and without it each one would start a pass.
func TestSchedulerHonoursTheMinimumInterval(t *testing.T) {
	sc := newScheduled(t, 30*time.Second)
	a := sc.addFile("poster kampus.psd", baseTime, artwork(3))
	b := sc.addFile("poster kampus fix.psd", baseTime.Add(30*time.Minute), nearlyIdentical(3))

	sc.notice(true)
	sc.notice(false)
	if sc.passes() != 1 {
		t.Fatalf("%d pengelompokan; mau 1", sc.passes())
	}
	sc.requireSameAsset(a, b)

	// More work arrives, but too soon.
	c := sc.addFile("poster kampus revisi 3.psd", baseTime.Add(time.Hour), artwork(3))
	sc.notice(true)
	sc.advance(10 * time.Second)
	sc.notice(false)
	if sc.passes() != 1 {
		t.Errorf("%d pengelompokan setelah 10 detik; lantai 30 detik dilewati", sc.passes())
	}
	if !sc.s.Pending() {
		t.Error("pekerjaan tertunda hilang padahal belum dikerjakan")
	}

	// Past the floor it runs, and the work that was waiting is done.
	sc.advance(25 * time.Second)
	sc.notice(false)
	if sc.passes() != 2 {
		t.Fatalf("%d pengelompokan setelah lantai terlewati; mau 2", sc.passes())
	}
	sc.requireSameAsset(a, b, c)
}

// A pass thrown away as stale has not dealt with what made the catalogue dirty,
// so the work must stay on the list rather than being quietly forgotten.
func TestSchedulerKeepsWorkPendingAfterAStalePass(t *testing.T) {
	sc := newScheduled(t, 0)
	a := sc.addFile("poster kampus.psd", baseTime, artwork(3))
	b := sc.addFile("poster kampus fix.psd", baseTime.Add(30*time.Minute), nearlyIdentical(3))

	once := false
	sc.g.beforeApply = func() {
		if once {
			return
		}
		once = true
		if _, err := sc.db.ExecContext(sc.t.Context(),
			`UPDATE observed_files SET last_seen_at = last_seen_at + 1 WHERE path = ?`,
			a); err != nil {
			t.Errorf("geser generasi: %v", err)
		}
	}

	sc.notice(true)
	sc.notice(false)

	if err := sc.lastErr(); !errors.Is(err, ErrStale) {
		t.Fatalf("pengelompokan = %v, mau ErrStale", err)
	}
	if !Stale(sc.lastErr()) {
		t.Error("Stale() tidak mengenali ErrStale")
	}
	if !sc.s.Pending() {
		t.Error("pekerjaan tertunda dibuang padahal pengelompokannya dibatalkan; " +
			"perubahannya tidak akan pernah dikelompokkan")
	}
	sc.requireDifferentAssets(a, b)

	// The next quiet scan picks it up and finishes the job.
	sc.notice(false)
	if err := sc.lastErr(); err != nil {
		t.Fatalf("pengelompokan kedua: %v", err)
	}
	sc.requireSameAsset(a, b)
	if sc.s.Pending() {
		t.Error("masih tertunda setelah berhasil")
	}
}

// Notice must not do the grouping on the caller's goroutine: a scan cycle comes
// round every second and a pass can take the better part of a minute.
func TestSchedulerDoesNotBlockTheScan(t *testing.T) {
	sc := newScheduled(t, 0)
	sc.addMany(1500)

	release := make(chan struct{})
	entered := make(chan struct{})
	sc.g.beforeApply = func() {
		close(entered)
		<-release
	}

	sc.s.Notice(t.Context(), true)

	start := time.Now()
	sc.s.Notice(t.Context(), false) // this is the one that starts a pass
	returned := time.Since(start)

	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		close(release)
		t.Fatal("pengelompokan tidak pernah mulai")
	}

	if returned > 2*time.Second {
		t.Errorf("Notice butuh %v untuk kembali; pemindaian akan tertahan di belakangnya",
			returned.Round(time.Millisecond))
	}
	t.Logf("Notice kembali dalam %v sementara pengelompokan masih berjalan",
		returned.Round(time.Millisecond))

	close(release)
	if err := sc.s.Wait(t.Context()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// Only one pass at a time, even when quiet scans keep arriving.
func TestSchedulerRunsOnePassAtATime(t *testing.T) {
	sc := newScheduled(t, 0)
	sc.addFile("poster kampus.psd", baseTime, artwork(3))
	sc.addFile("poster kampus fix.psd", baseTime.Add(30*time.Minute), nearlyIdentical(3))

	release := make(chan struct{})
	inFlight := make(chan struct{}, 8)
	sc.g.beforeApply = func() {
		inFlight <- struct{}{}
		<-release
		<-inFlight
	}

	sc.s.Notice(t.Context(), true)
	for i := 0; i < 5; i++ {
		sc.s.Notice(t.Context(), false)
	}

	time.Sleep(50 * time.Millisecond)
	if n := len(inFlight); n > 1 {
		close(release)
		t.Fatalf("%d pengelompokan terbang bersamaan; mau paling banyak 1", n)
	}

	close(release)
	if err := sc.s.Wait(t.Context()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if sc.passes() != 1 {
		t.Errorf("%d pengelompokan; lima pemindaian tenang tidak boleh jadi lima pengelompokan",
			sc.passes())
	}
}
