package grouping

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Scheduler decides when a regrouping pass should run.
//
// Grouping is not part of ingesting a file, and wiring it there would have been
// the obvious mistake. Scoring is quadratic in the number of tracked files: on a
// ten-thousand-file archive one pass is around forty seconds of arithmetic,
// while a watch cycle comes round every second. Calling it from inside a scan
// means, past a couple of thousand files, a scan that can never catch up with
// itself — and the slowdown arrives gradually, as the user's archive grows,
// long after anyone would think to look for it.
//
// So it runs beside ingesting rather than inside it:
//
//   - a scan that changed something marks the catalogue dirty and runs nothing,
//     because someone is clearly still working;
//   - a scan that changed nothing is the signal that they have stopped, and that
//     is when a pass starts;
//   - a floor on how often a pass may start keeps a busy afternoon of saves from
//     queueing one pass behind another;
//   - passes run on their own goroutine, so the watch cycle keeps its cadence
//     whatever the archive size.
//
// A pass aborted by ErrStale leaves the catalogue dirty, so the next quiet scan
// simply tries again.
type Scheduler struct {
	g           *Grouper
	minInterval time.Duration
	now         func() time.Time

	// onDone reports every finished pass, for the interface to show and for
	// tests to wait on. Errors arrive here rather than being swallowed.
	onDone func(Result, error)

	mu      sync.Mutex
	dirty   bool
	running bool
	lastRun time.Time

	// idle is closed-and-replaced to signal that nothing is in flight, so Wait
	// does not have to poll.
	idle chan struct{}
}

// DefaultMinInterval is the shortest gap between the start of one pass and the
// start of the next.
//
// Half a minute, for how the work actually arrives: a designer saves repeatedly
// for an hour and then stops. Every pause between saves reads as "they have
// stopped" to a scan, and without a floor a long session would spend more time
// regrouping than watching. Long enough to collapse a burst of saves into one
// pass; short enough that a newly dropped folder is grouped while the user is
// still looking at it.
const DefaultMinInterval = 30 * time.Second

// SchedulerOption configures a Scheduler.
type SchedulerOption func(*Scheduler)

// WithMinInterval sets the floor between the starts of two passes.
func WithMinInterval(d time.Duration) SchedulerOption {
	return func(s *Scheduler) { s.minInterval = d }
}

// WithSchedulerClock replaces the time source.
func WithSchedulerClock(now func() time.Time) SchedulerOption {
	return func(s *Scheduler) { s.now = now }
}

// WithOnDone registers a callback for the outcome of every pass, including
// ErrStale and cancellation.
func WithOnDone(fn func(Result, error)) SchedulerOption {
	return func(s *Scheduler) { s.onDone = fn }
}

// NewScheduler returns a Scheduler driving g.
func NewScheduler(g *Grouper, opts ...SchedulerOption) *Scheduler {
	s := &Scheduler{
		g:           g,
		minInterval: DefaultMinInterval,
		now:         time.Now,
		idle:        make(chan struct{}),
	}
	close(s.idle) // nothing in flight yet
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Notice takes the outcome of one scan. Wire it to ingest.WithAfterScan.
//
// It never blocks and never does the grouping itself: at most it starts a
// goroutine. ctx is the application's, so closing the window cancels a pass
// already in flight.
func (s *Scheduler) Notice(ctx context.Context, changed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if changed {
		// Still working. Remember that there is something to do and get out of
		// the way.
		s.dirty = true
		return
	}
	if !s.dirty || s.running {
		return
	}
	if !s.lastRun.IsZero() && s.now().Sub(s.lastRun) < s.minInterval {
		return
	}

	s.running = true
	s.lastRun = s.now()
	s.idle = make(chan struct{})
	go s.run(ctx, s.idle)
}

// run performs one pass and reports it.
//
// The order of the last three steps is load-bearing. Reporting happens first,
// then the pass stops counting as in flight, then idle is signalled — so by the
// time Wait can return, the result has already reached onDone. Marking it
// finished any earlier would let a caller wait for the pass and then read a
// record that is not there yet, which is a flaky test at best and a wrong number
// on screen at worst. onDone runs outside the lock so a callback is free to ask
// the scheduler about itself.
func (s *Scheduler) run(ctx context.Context, done chan struct{}) {
	result, err := s.g.Regroup(ctx)

	if s.onDone != nil {
		s.onDone(result, err)
	}

	s.mu.Lock()
	s.running = false
	// A pass thrown away as stale, or cancelled on the way out, has not dealt
	// with what made the catalogue dirty, so the flag stays up and the next
	// quiet scan tries again. Anything else has.
	if err == nil {
		s.dirty = false
	}
	s.mu.Unlock()

	close(done)
}

// Wait blocks until no pass is in flight, or ctx is done.
//
// When it returns nil, every pass that had started has finished and been
// reported through WithOnDone. For shutting down tidily and for tests. It does
// not stop anything: cancel the context passed to Notice for that.
func (s *Scheduler) Wait(ctx context.Context) error {
	for {
		s.mu.Lock()
		idle := s.idle
		running := s.running
		s.mu.Unlock()

		if !running {
			return nil
		}
		select {
		case <-idle:
			// Loop round: another pass may have started in the meantime.
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Pending reports whether there is grouping work waiting for a quiet scan.
func (s *Scheduler) Pending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dirty
}

// Stale reports whether err is a pass that was thrown away rather than a fault.
func Stale(err error) bool { return errors.Is(err, ErrStale) }
