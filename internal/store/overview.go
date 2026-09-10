package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Queries that exist for the interface rather than for the layers below it: one
// row per thing the user sees, assembled in SQL so a list of two hundred works
// does not become six hundred round trips.

// AssetCard is one work as the overview shows it: its newest version, and
// whether that version has a picture.
type AssetCard struct {
	AssetID   int64
	Name      string
	UpdatedAt time.Time

	LatestVersionID  int64
	LatestHash       string
	LatestSize       int64
	LatestObservedAt time.Time

	// ContentPresent is false once retention has discarded the bytes behind the
	// newest version. The row stays either way — the timeline may never have a
	// hole in it — so the interface has to be able to say so.
	ContentPresent bool

	// PreviewStatus is "" when no attempt has been recorded yet, as opposed to
	// one that was made and came to nothing.
	PreviewStatus  PreviewStatus
	PreviewFormat  string
	AlphaFlattened bool

	Versions int
	Files    int
}

// AssetCards returns every work, newest activity first.
func AssetCards(ctx context.Context, db DBTX) ([]AssetCard, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT a.id, a.name, a.updated_at,
		       v.id, v.file_hash, v.size, v.observed_at, v.content_present,
		       COALESCE(p.status, ''), COALESCE(p.format, ''),
		       COALESCE(p.alpha_flattened, 0),
		       (SELECT count(*) FROM versions       WHERE asset_id = a.id),
		       (SELECT count(*) FROM observed_files WHERE asset_id = a.id)
		FROM assets a
		JOIN versions v ON v.id = (
			SELECT id FROM versions
			WHERE asset_id = a.id
			ORDER BY observed_at DESC, id DESC
			LIMIT 1
		)
		LEFT JOIN previews p ON p.file_hash = v.file_hash
		ORDER BY a.updated_at DESC, a.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: daftar karya: %w", err)
	}
	defer rows.Close()

	var out []AssetCard
	for rows.Next() {
		var (
			c         AssetCard
			updated   int64
			observed  int64
			present   int
			status    string
			flattened int
		)
		if err := rows.Scan(&c.AssetID, &c.Name, &updated,
			&c.LatestVersionID, &c.LatestHash, &c.LatestSize, &observed, &present,
			&status, &c.PreviewFormat, &flattened,
			&c.Versions, &c.Files); err != nil {
			return nil, fmt.Errorf("store: baca karya: %w", err)
		}
		c.UpdatedAt = time.Unix(0, updated)
		c.LatestObservedAt = time.Unix(0, observed)
		c.ContentPresent = present == 1
		c.PreviewStatus = PreviewStatus(status)
		c.AlphaFlattened = flattened == 1
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: daftar karya: %w", err)
	}
	return out, nil
}

// TimelineEntry is one version as the timeline shows it, with its picture's
// outcome already joined on.
type TimelineEntry struct {
	Version Version

	PreviewStatus  PreviewStatus
	PreviewFormat  string
	AlphaFlattened bool
	PreviewErr     string
}

// Timeline returns every version of one asset, newest first.
//
// Versions whose content has been released are included, and must be: the
// timeline may never have a hole in it. ContentPresent on each entry is how the
// interface tells them apart.
func Timeline(ctx context.Context, db DBTX, assetID int64) ([]TimelineEntry, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT v.id, v.asset_id, v.file_hash, v.size, v.observed_at, v.modified_at,
		       v.source_path, v.file_key, v.content_present,
		       COALESCE(v.content_released_at, 0), v.pinned,
		       COALESCE(p.status, ''), COALESCE(p.format, ''),
		       COALESCE(p.alpha_flattened, 0), COALESCE(p.error, '')
		FROM versions v
		LEFT JOIN previews p ON p.file_hash = v.file_hash
		WHERE v.asset_id = ?
		ORDER BY v.observed_at DESC, v.id DESC`, assetID)
	if err != nil {
		return nil, fmt.Errorf("store: baca linimasa karya %d: %w", assetID, err)
	}
	defer rows.Close()

	var out []TimelineEntry
	for rows.Next() {
		var (
			e         TimelineEntry
			observed  int64
			modified  int64
			released  int64
			present   int
			pinned    int
			status    string
			flattened int
		)
		if err := rows.Scan(&e.Version.ID, &e.Version.AssetID, &e.Version.FileHash,
			&e.Version.Size, &observed, &modified, &e.Version.SourcePath,
			&e.Version.FileKey, &present, &released, &pinned,
			&status, &e.PreviewFormat, &flattened, &e.PreviewErr); err != nil {
			return nil, fmt.Errorf("store: baca versi: %w", err)
		}
		e.Version.ObservedAt = time.Unix(0, observed)
		e.Version.ModifiedAt = time.Unix(0, modified)
		if released != 0 {
			e.Version.ContentReleasedAt = time.Unix(0, released)
		}
		e.Version.ContentPresent = present == 1
		e.Version.Pinned = pinned == 1
		e.PreviewStatus = PreviewStatus(status)
		e.AlphaFlattened = flattened == 1
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: baca linimasa karya %d: %w", assetID, err)
	}
	return out, nil
}

// VersionByID reads one version.
func VersionByID(ctx context.Context, db DBTX, id int64) (Version, error) {
	var (
		v        Version
		observed int64
		modified int64
		released int64
		present  int
		pinned   int
	)
	err := db.QueryRowContext(ctx, `
		SELECT id, asset_id, file_hash, size, observed_at, modified_at,
		       source_path, file_key, content_present,
		       COALESCE(content_released_at, 0), pinned
		FROM versions WHERE id = ?`, id).
		Scan(&v.ID, &v.AssetID, &v.FileHash, &v.Size, &observed, &modified,
			&v.SourcePath, &v.FileKey, &present, &released, &pinned)
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, fmt.Errorf("%w: versi %d", ErrNotFound, id)
	}
	if err != nil {
		return Version{}, fmt.Errorf("store: baca versi %d: %w", id, err)
	}
	v.ObservedAt = time.Unix(0, observed)
	v.ModifiedAt = time.Unix(0, modified)
	if released != 0 {
		v.ContentReleasedAt = time.Unix(0, released)
	}
	v.ContentPresent = present == 1
	v.Pinned = pinned == 1
	return v, nil
}

// SetVersionPinned marks or unmarks a version as protected from thinning.
//
// A pinned version is immune, without exception — it is one of the three
// immunities retention must honour, alongside the newest version of each asset
// and anything carrying an open ticket. Unpinning is allowed: the user may
// change their mind, and nothing is destroyed at the moment of unpinning.
func SetVersionPinned(ctx context.Context, db DBTX, id int64, pinned bool) error {
	flag := 0
	if pinned {
		flag = 1
	}
	res, err := db.ExecContext(ctx,
		`UPDATE versions SET pinned = ? WHERE id = ?`, flag, id)
	if err != nil {
		return fmt.Errorf("store: tandai versi %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: hitung versi yang ditandai: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: versi %d", ErrNotFound, id)
	}
	return nil
}
