package store

import (
	"context"
	"fmt"
	"path/filepath"
	"time"
)

// WatchedFolder is one folder the user pointed CIT at.
type WatchedFolder struct {
	Path    string
	AddedAt time.Time
}

// AddWatchedFolder starts watching a folder. Adding one twice is not an error and
// does not move it in the list.
//
// The path is cleaned before it is stored, so the same folder named two
// different ways cannot be watched twice — which would scan everything under it
// twice and settle every save against two tracking rows.
func AddWatchedFolder(ctx context.Context, db DBTX, path string, now time.Time) error {
	clean := filepath.Clean(path)
	if clean == "" || clean == "." {
		return fmt.Errorf("store: folder yang diawasi tidak boleh kosong")
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO watched_folders (path, added_at) VALUES (?, ?)
		ON CONFLICT(path) DO NOTHING`, clean, now.UnixNano()); err != nil {
		return fmt.Errorf("store: tambah folder yang diawasi: %w", err)
	}
	return nil
}

// RemoveWatchedFolder stops watching a folder.
//
// Only the watching stops. Every asset, version and thumbnail that came from it
// stays exactly where it is: a folder the user no longer wants scanned is not a
// folder whose history they wanted erased, and nothing here may be the thing
// that throws history away.
func RemoveWatchedFolder(ctx context.Context, db DBTX, path string) error {
	if _, err := db.ExecContext(ctx,
		`DELETE FROM watched_folders WHERE path = ?`, filepath.Clean(path)); err != nil {
		return fmt.Errorf("store: lepas folder yang diawasi: %w", err)
	}
	return nil
}

// WatchedFolders returns the folders being watched, in the order they were added.
func WatchedFolders(ctx context.Context, db DBTX) ([]WatchedFolder, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT path, added_at FROM watched_folders ORDER BY added_at, path`)
	if err != nil {
		return nil, fmt.Errorf("store: daftar folder yang diawasi: %w", err)
	}
	defer rows.Close()

	var out []WatchedFolder
	for rows.Next() {
		var (
			f     WatchedFolder
			added int64
		)
		if err := rows.Scan(&f.Path, &added); err != nil {
			return nil, fmt.Errorf("store: baca folder yang diawasi: %w", err)
		}
		f.AddedAt = time.Unix(0, added)
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: daftar folder yang diawasi: %w", err)
	}
	return out, nil
}
