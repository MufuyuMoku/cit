package cmd

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/MufuyuMoku/cit/internal/grouping"
	"github.com/MufuyuMoku/cit/internal/ingest"
	"github.com/MufuyuMoku/cit/internal/preview"
	"github.com/MufuyuMoku/cit/internal/store"
	"github.com/MufuyuMoku/cit/internal/vault"
)

const previewDirName = preview.DirName

// service is every layer of CIT, opened and wired together.
//
// Up to this milestone nothing assembled them: each package had tests and no
// caller. This is the caller.
type service struct {
	layout layout

	db       *sql.DB
	vault    *vault.Vault
	previews *preview.Generator
	ingester *ingest.Ingester
	grouper  *grouping.Grouper
	sched    *grouping.Scheduler

	// watchCancel stops the current watch loop. It is replaced whenever the set
	// of watched folders changes, because ingest.Watch is given its roots once.
	mu          sync.Mutex
	watchCancel context.CancelFunc
	watchDone   chan struct{}

	// scanErr holds why watching stopped, if it did. Shown in the interface
	// rather than raised: CIT does not interrupt.
	scanErr string
}

// openService brings up the whole stack. Nothing is started yet.
func openService() (*service, error) {
	l, err := resolveLayout()
	if err != nil {
		return nil, err
	}

	db, err := store.Open(l.database)
	if err != nil {
		return nil, err
	}

	v, err := vault.Open(l.vault, db)
	if err != nil {
		db.Close()
		return nil, err
	}

	previews, err := preview.New(l.previews, db)
	if err != nil {
		v.Close()
		db.Close()
		return nil, err
	}

	s := &service{
		layout:   l,
		db:       db,
		vault:    v,
		previews: previews,
	}

	// Grouping reads thumbnails, never the originals: retention will one day
	// have thinned those away.
	s.grouper = grouping.New(db, previews)
	s.sched = grouping.NewScheduler(s.grouper,
		grouping.WithOnDone(func(r grouping.Result, err error) {
			switch {
			case err == nil:
				if r.Moved > 0 || r.AssetsRemoved > 0 {
					log.Printf("cit: pengelompokan: %d berkas dipindahkan, %d karya kosong dihapus",
						r.Moved, r.AssetsRemoved)
				}
			case grouping.Stale(err):
				// Normal: the catalogue moved while the pass was thinking, so it
				// threw its conclusions away. The next quiet scan retries.
			case errors.Is(err, context.Canceled):
				// Shutting down.
			default:
				log.Printf("cit: pengelompokan gagal: %v", err)
			}
		}),
	)

	// The hook is the only thing joining ingest and grouping; neither package
	// imports the other.
	s.ingester = ingest.New(db, v,
		ingest.WithPreviews(previews),
		ingest.WithAfterScan(s.sched.Notice),
	)

	return s, nil
}

// close shuts everything down in the reverse order it came up.
func (s *service) close() {
	if s.vault != nil {
		if err := s.vault.Close(); err != nil {
			log.Printf("cit: tutup brankas: %v", err)
		}
	}
	if s.db != nil {
		if err := s.db.Close(); err != nil {
			log.Printf("cit: tutup basis data: %v", err)
		}
	}
}

// restartWatching stops any running watch loop and starts one over the folders
// currently in the database.
//
// ctx is the application's, cancelled when the window closes. It reaches
// ingest.Scan and from there vault.Store unchanged, which is what makes closing
// CIT in the middle of a 200 MB file abort that store and leave nothing behind
// rather than finish a read nobody is waiting for.
func (s *service) restartWatching(ctx context.Context) error {
	folders, err := store.WatchedFolders(ctx, s.db)
	if err != nil {
		return err
	}

	s.stopWatching()

	if len(folders) == 0 {
		// Nothing to watch. Waking up once a second to scan no folders is just
		// a warm CPU.
		return nil
	}

	roots := make([]string, 0, len(folders))
	for _, f := range folders {
		roots = append(roots, f.Path)
	}

	watchCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	s.mu.Lock()
	s.watchCancel = cancel
	s.watchDone = done
	s.mu.Unlock()

	go func() {
		defer close(done)
		err := s.ingester.Watch(watchCtx, roots...)
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("cit: pengawasan berhenti: %v", err)
			s.mu.Lock()
			s.scanErr = err.Error()
			s.mu.Unlock()
		}
	}()

	return nil
}

// stopWatching cancels the watch loop and waits for it to return, so the vault
// is quiet before anything closes it.
func (s *service) stopWatching() {
	s.mu.Lock()
	cancel, done := s.watchCancel, s.watchDone
	s.watchCancel, s.watchDone = nil, nil
	s.mu.Unlock()

	if cancel == nil {
		return
	}
	cancel()

	// A Store that is mid-file aborts as soon as it notices; waiting is bounded
	// so a wedged read cannot hold the window open for ever.
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		log.Print("cit: pengawasan tidak berhenti dalam 10 detik; tetap lanjut menutup")
	}
}

// shutdown stops watching, lets any grouping pass finish, and closes everything.
func (s *service) shutdown() {
	s.stopWatching()

	// A pass in flight has already been cancelled along with the context it was
	// given; this only waits for it to unwind before the database closes under
	// it.
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.sched.Wait(waitCtx); err != nil {
		log.Printf("cit: pengelompokan belum selesai saat menutup: %v", err)
	}

	s.close()
}

// watching reports whether a watch loop is running.
func (s *service) watching() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.watchCancel != nil
}

// watchError reports why watching stopped, or "" if it has not.
func (s *service) watchError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scanErr
}
