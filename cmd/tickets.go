package cmd

import (
	"errors"
	"fmt"
	"time"

	"github.com/MufuyuMoku/cit/internal/store"
	"github.com/MufuyuMoku/cit/internal/ticket"
)

// TicketView is one ticket as a page shows it.
type TicketView struct {
	ID        int64  `json:"id"`
	Direction string `json:"direction"`
	Status    string `json:"status"`
	Note      string `json:"note"`
	Who       string `json:"who"`

	CreatedAt string `json:"createdAt"`
	FlaggedAt string `json:"flaggedAt"`
	ClosedAt  string `json:"closedAt"`

	// AgeDays is how long the ticket has been waiting, for the one direction
	// where that number is the point: six days with no word is what makes a
	// person follow something up.
	AgeDays int    `json:"ageDays"`
	AgeText string `json:"ageText"`

	AssetID   int64  `json:"assetId"`
	AssetName string `json:"assetName"`

	VersionID         int64  `json:"versionId"`
	VersionObservedAt string `json:"versionObservedAt"`
	FileName          string `json:"fileName"`
	Hash              string `json:"hash"`

	// ThumbURL points at the picture of the exact version the ticket is about,
	// not at the work's newest one. A ticket is about one save.
	ThumbURL string `json:"thumbUrl"`
}

// toTicketViews turns tracker views into what the interface reads, looking up
// each version's thumbnail on the way.
func (a *App) toTicketViews(views []ticket.View) ([]TicketView, error) {
	out := make([]TicketView, 0, len(views))
	for _, v := range views {
		tv := TicketView{
			ID:                v.ID,
			Direction:         string(v.Direction),
			Status:            string(v.Status),
			Note:              v.Note,
			Who:               v.Who,
			CreatedAt:         v.CreatedAt.Format(time.RFC3339),
			AgeDays:           int(v.Age.Hours() / 24),
			AgeText:           ageText(v.Age),
			AssetID:           v.AssetID,
			AssetName:         v.AssetName,
			VersionID:         v.VersionID,
			VersionObservedAt: v.VersionObservedAt.Format(time.RFC3339),
			FileName:          v.FileName,
			Hash:              shortHash(v.FileHash),
		}
		if !v.FlaggedAt.IsZero() {
			tv.FlaggedAt = v.FlaggedAt.Format(time.RFC3339)
		}
		if !v.ClosedAt.IsZero() {
			tv.ClosedAt = v.ClosedAt.Format(time.RFC3339)
		}

		preview, err := store.PreviewByFileHash(a.ctx, a.svc.db, v.FileHash)
		if err == nil {
			tv.ThumbURL = thumbURL(preview.Status, v.FileHash)
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}

		out = append(out, tv)
	}
	return out, nil
}

// ageText says how long something has been waiting, in the units people say out
// loud. Plain and unalarming: this is a fact, not a warning.
func ageText(d time.Duration) string {
	switch {
	case d < time.Hour:
		return "baru saja"
	case d < 24*time.Hour:
		return fmt.Sprintf("%d jam", int(d.Hours()))
	}
	days := int(d.Hours() / 24)
	if days == 1 {
		return "sehari"
	}
	return fmt.Sprintf("%d hari", days)
}

// OpenTicket records a new ticket against one version.
//
// direction is "waiting_on_them" or "waiting_on_me". No asset is passed: a ticket
// belongs to whatever work its version belongs to, which is what lets it follow
// the file when grouping moves it.
func (a *App) OpenTicket(versionID int64, direction, note, who string) (TicketView, error) {
	if a.svc == nil {
		return TicketView{}, errNotReady
	}

	tk, err := a.svc.tickets.Open(a.ctx, versionID, store.Direction(direction), note, who)
	if err != nil {
		if errors.Is(err, ticket.ErrNoNote) {
			return TicketView{}, errors.New("tulis dulu apa yang ditunggu, supaya nanti tiketnya masih berarti")
		}
		return TicketView{}, err
	}

	// Read it back the way the page will show it, so the caller does not have to
	// guess at the derived fields.
	assetID, err := store.AssetIDForTicket(a.ctx, a.svc.db, tk.ID)
	if err != nil {
		return TicketView{}, err
	}
	forAsset, err := a.svc.tickets.ForAsset(a.ctx, assetID)
	if err != nil {
		return TicketView{}, err
	}
	converted, err := a.toTicketViews(forAsset)
	if err != nil {
		return TicketView{}, err
	}
	for _, v := range converted {
		if v.ID == tk.ID {
			return v, nil
		}
	}
	return TicketView{}, fmt.Errorf("tiket %d tidak terbaca setelah dibuat", tk.ID)
}

// CloseTicket marks a ticket finished. Only a person does this.
func (a *App) CloseTicket(ticketID int64) error {
	if a.svc == nil {
		return errNotReady
	}
	return a.svc.tickets.Close(a.ctx, ticketID)
}

// ReopenTicket puts a ticket back in flight — it was not done after all.
func (a *App) ReopenTicket(ticketID int64) error {
	if a.svc == nil {
		return errNotReady
	}
	return a.svc.tickets.Reopen(a.ctx, ticketID)
}

// WaitingOnThem lists what the user is waiting on someone else for, longest wait
// first.
func (a *App) WaitingOnThem() ([]TicketView, error) {
	return a.waiting(store.WaitingOnThem)
}

// WaitingOnMe lists work the user owes, longest wait first.
func (a *App) WaitingOnMe() ([]TicketView, error) {
	return a.waiting(store.WaitingOnMe)
}

func (a *App) waiting(d store.Direction) ([]TicketView, error) {
	if a.svc == nil {
		return nil, errNotReady
	}
	views, err := a.svc.tickets.Waiting(a.ctx, d)
	if err != nil {
		return nil, err
	}
	return a.toTicketViews(views)
}

// ReviewInbox returns everything waiting for the user's judgement.
//
// Nothing here arrived by interrupting them, and nothing here is urgent by
// construction: these are tickets a newer version suggests may be finished, and
// the suggestion keeps until it is looked at.
func (a *App) ReviewInbox() ([]TicketView, error) {
	if a.svc == nil {
		return nil, errNotReady
	}
	views, err := a.svc.tickets.ReviewInbox(a.ctx)
	if err != nil {
		return nil, err
	}
	return a.toTicketViews(views)
}

// TicketsForAsset returns every ticket on one work, closed ones included.
func (a *App) TicketsForAsset(assetID int64) ([]TicketView, error) {
	if a.svc == nil {
		return nil, errNotReady
	}
	views, err := a.svc.tickets.ForAsset(a.ctx, assetID)
	if err != nil {
		return nil, err
	}
	return a.toTicketViews(views)
}
