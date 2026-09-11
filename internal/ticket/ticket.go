package ticket

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/MufuyuMoku/cit/internal/store"
)

// ErrNoNote is returned when a ticket is created with nothing written on it.
//
// A ticket with no note is a reminder that something was once important and no
// record of what, which is worse than no ticket at all: it cannot be acted on
// and cannot be dismissed with confidence.
var ErrNoNote = errors.New("ticket: tiket butuh catatan singkat tentang apa yang ditunggu")

// Tracker owns tickets and the review inbox.
type Tracker struct {
	db  *sql.DB
	now func() time.Time
}

// Option configures a Tracker.
type Option func(*Tracker)

// WithClock replaces the time source.
func WithClock(now func() time.Time) Option {
	return func(t *Tracker) { t.now = now }
}

// New returns a Tracker.
func New(db *sql.DB, opts ...Option) *Tracker {
	t := &Tracker{db: db, now: time.Now}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Open records a new ticket against one recorded save.
//
// It takes a version, never a path and never an asset. A path would break the
// moment the file was renamed; an asset would be a second copy of something the
// version already knows, and would go stale the first time grouping moved that
// version somewhere else.
func (t *Tracker) Open(ctx context.Context, versionID int64, direction store.Direction, note, who string) (store.Ticket, error) {
	if note == "" {
		return store.Ticket{}, ErrNoNote
	}
	switch direction {
	case store.WaitingOnThem, store.WaitingOnMe:
	default:
		return store.Ticket{}, fmt.Errorf("ticket: arah tidak dikenal: %q", direction)
	}

	// Checked here so the caller gets ErrNotFound rather than a constraint
	// failure. The database refuses it either way — see the tickets_need_a_version
	// trigger — and that is the guarantee; this is only the better message.
	if _, err := store.VersionByID(ctx, t.db, versionID); err != nil {
		return store.Ticket{}, err
	}

	now := t.now()
	id, err := store.AddTicket(ctx, t.db, store.Ticket{
		VersionID: versionID,
		Direction: direction,
		Status:    store.TicketOpen,
		Note:      note,
		Who:       who,
		CreatedAt: now,
	})
	if err != nil {
		return store.Ticket{}, err
	}
	return store.TicketByID(ctx, t.db, id)
}

// Close marks a ticket finished. Only a person ever does this: a newer version
// moves a ticket into the review inbox and stops there.
func (t *Tracker) Close(ctx context.Context, ticketID int64) error {
	return store.SetTicketStatus(ctx, t.db, ticketID, store.TicketClosed, t.now())
}

// Reopen puts a ticket back in flight, whether it was closed or was sitting in
// the review inbox and turned out not to be done after all.
func (t *Tracker) Reopen(ctx context.Context, ticketID int64) error {
	return store.SetTicketStatus(ctx, t.db, ticketID, store.TicketOpen, t.now())
}

// View is one ticket with the things the interface needs beside it.
type View struct {
	store.Ticket

	// AssetID is derived through the version, so it is always the work the
	// ticket's version belongs to right now.
	AssetID   int64
	AssetName string

	// FileName is the file the version came from, for saying which of a work's
	// files this is about.
	FileName string
	FileHash string

	// VersionObservedAt is when the save this ticket hangs off was recorded.
	VersionObservedAt time.Time

	// Age is how long the ticket has been waiting. For a "waiting on them" this
	// is the number that matters: six days with no word is what makes someone
	// pick up the phone.
	Age time.Duration
}

// Waiting returns outstanding tickets running one direction, longest wait first.
func (t *Tracker) Waiting(ctx context.Context, d store.Direction) ([]View, error) {
	tickets, err := store.OpenTicketsByDirection(ctx, t.db, d)
	if err != nil {
		return nil, err
	}
	return t.decorate(ctx, tickets)
}

// ReviewInbox returns everything waiting for the user's judgement.
//
// One page, opened when they want it. Nothing here interrupted them to arrive
// and nothing here is urgent by construction: these are tickets a newer version
// suggests may be finished, and the suggestion keeps until it is looked at.
func (t *Tracker) ReviewInbox(ctx context.Context) ([]View, error) {
	tickets, err := store.TicketsByStatus(ctx, t.db, store.TicketMaybeDone)
	if err != nil {
		return nil, err
	}
	return t.decorate(ctx, tickets)
}

// ForAsset returns every ticket on one work, including closed ones, so a work's
// page can show what has already been dealt with.
func (t *Tracker) ForAsset(ctx context.Context, assetID int64) ([]View, error) {
	tickets, err := store.TicketsForAsset(ctx, t.db, assetID)
	if err != nil {
		return nil, err
	}
	return t.decorate(ctx, tickets)
}

// decorate fills in what each ticket's version and asset say about it.
func (t *Tracker) decorate(ctx context.Context, tickets []store.Ticket) ([]View, error) {
	now := t.now()
	names := map[int64]string{}
	out := make([]View, 0, len(tickets))

	for _, tk := range tickets {
		v, err := store.VersionByID(ctx, t.db, tk.VersionID)
		if err != nil {
			// A version that cannot be read is a broken database, not a missing
			// ticket: versions are permanent and the foreign key is enforced.
			return nil, fmt.Errorf("ticket: versi %d untuk tiket %d tidak terbaca: %w",
				tk.VersionID, tk.ID, err)
		}

		name, ok := names[v.AssetID]
		if !ok {
			asset, err := store.AssetByID(ctx, t.db, v.AssetID)
			if err != nil {
				return nil, err
			}
			name = asset.Name
			names[v.AssetID] = name
		}

		out = append(out, View{
			Ticket:            tk,
			AssetID:           v.AssetID,
			AssetName:         name,
			FileName:          baseName(v.FileKey),
			FileHash:          v.FileHash,
			VersionObservedAt: v.ObservedAt,
			Age:               now.Sub(tk.CreatedAt),
		})
	}
	return out, nil
}

// baseName is filepath.Base without importing path handling for one line, and
// without caring which separator the path was written with: a database can hold
// paths from either platform if it travels.
func baseName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[i+1:]
		}
	}
	return path
}
