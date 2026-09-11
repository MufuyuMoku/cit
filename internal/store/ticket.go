package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Direction is which way the waiting runs.
type Direction string

const (
	// WaitingOnThem: the user has done their part and is waiting for someone
	// else. Shown with its age, because "six days with no word" is the fact that
	// makes a person pick up the phone.
	WaitingOnThem Direction = "waiting_on_them"

	// WaitingOnMe: someone else is waiting on the user. This is work owed.
	WaitingOnMe Direction = "waiting_on_me"
)

// TicketStatus is where a ticket sits.
type TicketStatus string

const (
	// TicketOpen: still in flight.
	TicketOpen TicketStatus = "open"

	// TicketMaybeDone: a newer version arrived on this work, so the ticket may
	// have been dealt with. It is in the review inbox waiting to be looked at.
	// Nothing but a person moves it from here.
	TicketMaybeDone TicketStatus = "maybe_done"

	// TicketClosed: the user said it was finished.
	TicketClosed TicketStatus = "closed"
)

// Ticket is one piece of work in flight, attached to one recorded save.
type Ticket struct {
	ID        int64
	VersionID int64
	Direction Direction
	Status    TicketStatus
	Note      string
	Who       string
	CreatedAt time.Time

	// FlaggedAt is when a newer version moved this into the review inbox. Zero
	// while the ticket has never been flagged.
	FlaggedAt time.Time

	// ClosedAt is when the user closed it. Zero while it is not closed.
	ClosedAt time.Time
}

// IsOpen reports whether a ticket still counts as outstanding.
//
// A ticket the user has not closed is outstanding, and that deliberately
// includes maybe_done: a newer version is a hint, not a decision, and until
// someone has actually looked at it the work is not finished. Retention leans on
// this — see FileHashesWithOpenTickets.
func (t Ticket) IsOpen() bool { return t.Status != TicketClosed }

// AddTicket records a new ticket against one version.
//
// No asset id is taken, and that is not an omission: a ticket belongs to
// whatever work its version currently belongs to. Grouping repoints versions
// between assets routinely, and a ticket that carried its own copy of the asset
// would be stranded on the old one the first time that happened.
func AddTicket(ctx context.Context, db DBTX, t Ticket) (int64, error) {
	switch t.Direction {
	case WaitingOnThem, WaitingOnMe:
	default:
		return 0, fmt.Errorf("store: arah tiket tidak dikenal: %q", t.Direction)
	}
	if t.Status == "" {
		t.Status = TicketOpen
	}

	res, err := db.ExecContext(ctx, `
		INSERT INTO tickets (version_id, direction, status, note, who, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		t.VersionID, string(t.Direction), string(t.Status), t.Note, t.Who,
		t.CreatedAt.UnixNano())
	if err != nil {
		return 0, fmt.Errorf("store: catat tiket: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: id tiket: %w", err)
	}
	return id, nil
}

const ticketColumns = `id, version_id, direction, status, note, who, created_at,
	COALESCE(flagged_at, 0), COALESCE(closed_at, 0)`

func scanTicket(row interface{ Scan(...any) error }) (Ticket, error) {
	var (
		t         Ticket
		direction string
		status    string
		created   int64
		flagged   int64
		closed    int64
	)
	if err := row.Scan(&t.ID, &t.VersionID, &direction, &status, &t.Note, &t.Who,
		&created, &flagged, &closed); err != nil {
		return Ticket{}, err
	}
	t.Direction = Direction(direction)
	t.Status = TicketStatus(status)
	t.CreatedAt = time.Unix(0, created)
	if flagged != 0 {
		t.FlaggedAt = time.Unix(0, flagged)
	}
	if closed != 0 {
		t.ClosedAt = time.Unix(0, closed)
	}
	return t, nil
}

// TicketByID reads one ticket.
func TicketByID(ctx context.Context, db DBTX, id int64) (Ticket, error) {
	row := db.QueryRowContext(ctx,
		`SELECT `+ticketColumns+` FROM tickets WHERE id = ?`, id)

	t, err := scanTicket(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Ticket{}, fmt.Errorf("%w: tiket %d", ErrNotFound, id)
	}
	if err != nil {
		return Ticket{}, fmt.Errorf("store: baca tiket %d: %w", id, err)
	}
	return t, nil
}

// SetTicketStatus moves a ticket, recording when it was closed.
func SetTicketStatus(ctx context.Context, db DBTX, id int64, status TicketStatus, now time.Time) error {
	switch status {
	case TicketOpen, TicketMaybeDone, TicketClosed:
	default:
		return fmt.Errorf("store: status tiket tidak dikenal: %q", status)
	}

	var closedAt any
	if status == TicketClosed {
		closedAt = now.UnixNano()
	}

	res, err := db.ExecContext(ctx, `
		UPDATE tickets SET status = ?, closed_at = ? WHERE id = ?`,
		string(status), closedAt, id)
	if err != nil {
		return fmt.Errorf("store: ubah status tiket %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: hitung tiket yang berubah: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: tiket %d", ErrNotFound, id)
	}
	return nil
}

// TicketsForAsset returns every ticket belonging to a work, newest first.
//
// Joined through versions rather than read from a column here, which is what
// makes a ticket follow its version when grouping moves it.
func TicketsForAsset(ctx context.Context, db DBTX, assetID int64) ([]Ticket, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT `+prefixed("t")+`
		FROM tickets t
		JOIN versions v ON v.id = t.version_id
		WHERE v.asset_id = ?
		ORDER BY t.created_at DESC, t.id DESC`, assetID)
	if err != nil {
		return nil, fmt.Errorf("store: baca tiket karya %d: %w", assetID, err)
	}
	return collectTickets(rows, "baca tiket karya")
}

// TicketsByStatus returns every ticket in one state, oldest first.
//
// Oldest first on purpose: the thing that has been waiting longest is the thing
// most worth looking at, and burying it under today's arrivals is how a queue
// stops being useful.
func TicketsByStatus(ctx context.Context, db DBTX, status TicketStatus) ([]Ticket, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT `+ticketColumns+`
		FROM tickets
		WHERE status = ?
		ORDER BY created_at, id`, string(status))
	if err != nil {
		return nil, fmt.Errorf("store: baca tiket berstatus %s: %w", status, err)
	}
	return collectTickets(rows, "baca tiket berstatus")
}

// OpenTicketsByDirection returns outstanding tickets running one way, oldest
// first. Tickets already in the review inbox are excluded: they are waiting on
// the user's judgement, not on the world.
func OpenTicketsByDirection(ctx context.Context, db DBTX, d Direction) ([]Ticket, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT `+ticketColumns+`
		FROM tickets
		WHERE status = 'open' AND direction = ?
		ORDER BY created_at, id`, string(d))
	if err != nil {
		return nil, fmt.Errorf("store: baca tiket arah %s: %w", d, err)
	}
	return collectTickets(rows, "baca tiket arah")
}

// CountOpenTickets counts everything not yet closed.
func CountOpenTickets(ctx context.Context, db DBTX) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM tickets WHERE status <> 'closed'`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: hitung tiket terbuka: %w", err)
	}
	return n, nil
}

// FileHashesWithOpenTickets returns the content hashes that retention may never
// release.
//
// This exists now, before there is any retention to use it, so that M7 does not
// have to reshape the ticket schema to answer the question. "A version with an
// open ticket is immune from thinning" is an invariant with no exceptions, and
// thinning works by content hash — MarkContentReleased takes a file_hash and
// covers every version sharing it — so a hash is what has to come back.
//
// Anything not closed counts, maybe_done included. A ticket sitting in the
// review inbox is one the user has not finished with, and discarding the bytes
// under it would answer their question for them by destroying the evidence.
func FileHashesWithOpenTickets(ctx context.Context, db DBTX) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT v.file_hash
		FROM tickets t
		JOIN versions v ON v.id = t.version_id
		WHERE t.status <> 'closed'
		ORDER BY v.file_hash`)
	if err != nil {
		return nil, fmt.Errorf("store: baca hash yang punya tiket terbuka: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, fmt.Errorf("store: baca hash tiket: %w", err)
		}
		out = append(out, hash)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: baca hash yang punya tiket terbuka: %w", err)
	}
	return out, nil
}

// VersionHasOpenTicket reports whether one version is held by a ticket.
func VersionHasOpenTicket(ctx context.Context, db DBTX, versionID int64) (bool, error) {
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM tickets WHERE version_id = ? AND status <> 'closed'`,
		versionID).Scan(&n); err != nil {
		return false, fmt.Errorf("store: periksa tiket versi %d: %w", versionID, err)
	}
	return n > 0, nil
}

// AssetIDForTicket reports which work a ticket currently belongs to, derived
// through its version rather than stored.
func AssetIDForTicket(ctx context.Context, db DBTX, ticketID int64) (int64, error) {
	var assetID int64
	err := db.QueryRowContext(ctx, `
		SELECT v.asset_id FROM tickets t
		JOIN versions v ON v.id = t.version_id
		WHERE t.id = ?`, ticketID).Scan(&assetID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: tiket %d", ErrNotFound, ticketID)
	}
	if err != nil {
		return 0, fmt.Errorf("store: cari karya tiket %d: %w", ticketID, err)
	}
	return assetID, nil
}

// prefixed qualifies the ticket columns for a query that joins.
func prefixed(alias string) string {
	return alias + `.id, ` + alias + `.version_id, ` + alias + `.direction, ` +
		alias + `.status, ` + alias + `.note, ` + alias + `.who, ` +
		alias + `.created_at, COALESCE(` + alias + `.flagged_at, 0), ` +
		`COALESCE(` + alias + `.closed_at, 0)`
}

func collectTickets(rows *sql.Rows, what string) ([]Ticket, error) {
	defer rows.Close()

	var out []Ticket
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			return nil, fmt.Errorf("store: %s: %w", what, err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: %s: %w", what, err)
	}
	return out, nil
}
