package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound is returned when a lookup finds no row.
var ErrNotFound = errors.New("store: tidak ada di basis data")

// DBTX is the subset of *sql.DB and *sql.Tx the catalogue queries need, so a
// caller can run any of them inside its own transaction.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Asset is one piece of work.
type Asset struct {
	ID        int64
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Version is one observed save of an asset.
//
// A Version row is permanent. When retention discards the chunks behind it,
// ContentPresent goes false and ContentReleasedAt is set; the row itself stays,
// so the timeline never has a hole in it.
type Version struct {
	ID                int64
	AssetID           int64
	FileHash          string
	Size              int64
	ObservedAt        time.Time
	ModifiedAt        time.Time
	SourcePath        string
	ContentPresent    bool
	ContentReleasedAt time.Time
	Pinned            bool
}

// ObservedFile is a path on disk currently being tracked. Unlike a Version it
// describes the present and may be updated or deleted freely.
type ObservedFile struct {
	Path           string
	AssetID        int64
	LastHash       string
	LastSize       int64
	LastModifiedAt time.Time
	LastSeenAt     time.Time

	// LastVerifiedAt is when the content was last read and hashed for real,
	// rather than assumed unchanged from its size and mtime. Meant to be shown
	// to the user: "checked 3 minutes ago" and "assumed unchanged since
	// Tuesday" are different promises, and CIT should not blur them.
	LastVerifiedAt time.Time
}

// --- assets -----------------------------------------------------------------

// CreateAsset inserts an asset and returns its id.
func CreateAsset(ctx context.Context, db DBTX, name string, now time.Time) (int64, error) {
	res, err := db.ExecContext(ctx,
		`INSERT INTO assets (name, created_at, updated_at) VALUES (?, ?, ?)`,
		name, now.UnixNano(), now.UnixNano())
	if err != nil {
		return 0, fmt.Errorf("store: buat karya %q: %w", name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: buat karya %q: %w", name, err)
	}
	return id, nil
}

// AssetByID reads one asset.
func AssetByID(ctx context.Context, db DBTX, id int64) (Asset, error) {
	var (
		a                    Asset
		createdAt, updatedAt int64
	)
	err := db.QueryRowContext(ctx,
		`SELECT id, name, created_at, updated_at FROM assets WHERE id = ?`, id).
		Scan(&a.ID, &a.Name, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Asset{}, fmt.Errorf("%w: karya %d", ErrNotFound, id)
	}
	if err != nil {
		return Asset{}, fmt.Errorf("store: baca karya %d: %w", id, err)
	}
	a.CreatedAt = time.Unix(0, createdAt)
	a.UpdatedAt = time.Unix(0, updatedAt)
	return a, nil
}

// TouchAsset moves an asset's updated_at forward.
func TouchAsset(ctx context.Context, db DBTX, id int64, now time.Time) error {
	if _, err := db.ExecContext(ctx,
		`UPDATE assets SET updated_at = ? WHERE id = ?`, now.UnixNano(), id); err != nil {
		return fmt.Errorf("store: perbarui karya %d: %w", id, err)
	}
	return nil
}

// CountAssets reports how many assets exist.
func CountAssets(ctx context.Context, db DBTX) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM assets`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: hitung karya: %w", err)
	}
	return n, nil
}

// --- versions ---------------------------------------------------------------

// AddVersion records one observed save and returns its id.
func AddVersion(ctx context.Context, db DBTX, v Version) (int64, error) {
	res, err := db.ExecContext(ctx, `
		INSERT INTO versions
			(asset_id, file_hash, size, observed_at, modified_at, source_path, content_present, pinned)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?)`,
		v.AssetID, v.FileHash, v.Size,
		v.ObservedAt.UnixNano(), v.ModifiedAt.UnixNano(), v.SourcePath,
		boolToInt(v.Pinned))
	if err != nil {
		return 0, fmt.Errorf("store: catat versi untuk karya %d: %w", v.AssetID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: catat versi untuk karya %d: %w", v.AssetID, err)
	}
	return id, nil
}

// VersionsByAsset returns an asset's timeline, oldest first. Versions whose
// content has been released are included: the timeline must never have a hole.
func VersionsByAsset(ctx context.Context, db DBTX, assetID int64) ([]Version, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, asset_id, file_hash, size, observed_at, modified_at,
		       source_path, content_present, content_released_at, pinned
		FROM versions
		WHERE asset_id = ?
		ORDER BY observed_at, id`, assetID)
	if err != nil {
		return nil, fmt.Errorf("store: baca linimasa karya %d: %w", assetID, err)
	}
	defer rows.Close()

	var out []Version
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: baca linimasa karya %d: %w", assetID, err)
	}
	return out, nil
}

// LatestVersion returns the newest version of an asset.
func LatestVersion(ctx context.Context, db DBTX, assetID int64) (Version, error) {
	row := db.QueryRowContext(ctx, `
		SELECT id, asset_id, file_hash, size, observed_at, modified_at,
		       source_path, content_present, content_released_at, pinned
		FROM versions
		WHERE asset_id = ?
		ORDER BY observed_at DESC, id DESC
		LIMIT 1`, assetID)

	v, err := scanVersion(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, fmt.Errorf("%w: versi untuk karya %d", ErrNotFound, assetID)
	}
	return v, err
}

// CountVersions reports how many versions exist.
func CountVersions(ctx context.Context, db DBTX) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM versions`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: hitung versi: %w", err)
	}
	return n, nil
}

// MarkContentReleased records that the chunks behind a version are gone. The
// row survives; only the marker changes. This is the only correct way to
// express a thinned version — deleting the row is refused by the database.
func MarkContentReleased(ctx context.Context, db DBTX, fileHash string, now time.Time) (int64, error) {
	res, err := db.ExecContext(ctx, `
		UPDATE versions
		SET content_present = 0, content_released_at = ?
		WHERE file_hash = ? AND content_present = 1`,
		now.UnixNano(), fileHash)
	if err != nil {
		return 0, fmt.Errorf("store: tandai isi %s sudah dibuang: %w", shortHash(fileHash), err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: tandai isi %s sudah dibuang: %w", shortHash(fileHash), err)
	}
	return n, nil
}

// --- observed files ---------------------------------------------------------

// ObservedFileByPath looks up the tracking row for a path.
func ObservedFileByPath(ctx context.Context, db DBTX, path string) (ObservedFile, error) {
	row := db.QueryRowContext(ctx, `
		SELECT path, asset_id, last_hash, last_size, last_modified_at, last_seen_at, last_verified_at
		FROM observed_files WHERE path = ?`, path)

	f, err := scanObservedFile(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ObservedFile{}, fmt.Errorf("%w: jalur %s", ErrNotFound, path)
	}
	return f, err
}

// ObservedFilesByHash returns every tracked path whose last version had this
// content. Used to tell a rename from a fresh file.
func ObservedFilesByHash(ctx context.Context, db DBTX, hash string) ([]ObservedFile, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT path, asset_id, last_hash, last_size, last_modified_at, last_seen_at, last_verified_at
		FROM observed_files WHERE last_hash = ?`, hash)
	if err != nil {
		return nil, fmt.Errorf("store: cari jalur dengan isi %s: %w", shortHash(hash), err)
	}
	defer rows.Close()

	var out []ObservedFile
	for rows.Next() {
		f, err := scanObservedFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: cari jalur dengan isi %s: %w", shortHash(hash), err)
	}
	return out, nil
}

// PutObservedFile inserts or updates a tracking row.
func PutObservedFile(ctx context.Context, db DBTX, f ObservedFile) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO observed_files
			(path, asset_id, last_hash, last_size, last_modified_at, last_seen_at, last_verified_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			asset_id         = excluded.asset_id,
			last_hash        = excluded.last_hash,
			last_size        = excluded.last_size,
			last_modified_at = excluded.last_modified_at,
			last_seen_at     = excluded.last_seen_at,
			last_verified_at = excluded.last_verified_at`,
		f.Path, f.AssetID, f.LastHash, f.LastSize,
		f.LastModifiedAt.UnixNano(), f.LastSeenAt.UnixNano(), f.LastVerifiedAt.UnixNano())
	if err != nil {
		return fmt.Errorf("store: simpan jalur %s: %w", f.Path, err)
	}
	return nil
}

// DeleteObservedFile stops tracking a path. The asset and its versions stay:
// a file disappearing from disk does not erase its history.
func DeleteObservedFile(ctx context.Context, db DBTX, path string) error {
	if _, err := db.ExecContext(ctx,
		`DELETE FROM observed_files WHERE path = ?`, path); err != nil {
		return fmt.Errorf("store: hapus jalur %s: %w", path, err)
	}
	return nil
}

// ObservedFilesUnder returns every tracked path beginning with prefix.
func ObservedFilesUnder(ctx context.Context, db DBTX, prefix string) ([]ObservedFile, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT path, asset_id, last_hash, last_size, last_modified_at, last_seen_at, last_verified_at
		FROM observed_files
		WHERE path LIKE ? ESCAPE '\'
		ORDER BY path`, escapeLike(prefix)+"%")
	if err != nil {
		return nil, fmt.Errorf("store: daftar jalur di bawah %s: %w", prefix, err)
	}
	defer rows.Close()

	var out []ObservedFile
	for rows.Next() {
		f, err := scanObservedFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: daftar jalur di bawah %s: %w", prefix, err)
	}
	return out, nil
}

// ObservedFilesByAsset returns the paths tracked for one asset, so the user
// interface can show where a work lives and, for each place, when its content
// was last genuinely verified.
func ObservedFilesByAsset(ctx context.Context, db DBTX, assetID int64) ([]ObservedFile, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT path, asset_id, last_hash, last_size, last_modified_at, last_seen_at, last_verified_at
		FROM observed_files
		WHERE asset_id = ?
		ORDER BY path`, assetID)
	if err != nil {
		return nil, fmt.Errorf("store: daftar jalur karya %d: %w", assetID, err)
	}
	defer rows.Close()

	var out []ObservedFile
	for rows.Next() {
		f, err := scanObservedFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: daftar jalur karya %d: %w", assetID, err)
	}
	return out, nil
}

// ObservedFilesVerifiedBefore returns tracked paths whose content has not been
// re-read since cutoff, oldest first. Two callers: the periodic verification
// pass, and any view that wants to tell the user which files are only assumed
// unchanged.
func ObservedFilesVerifiedBefore(ctx context.Context, db DBTX, cutoff time.Time, limit int) ([]ObservedFile, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT path, asset_id, last_hash, last_size, last_modified_at, last_seen_at, last_verified_at
		FROM observed_files
		WHERE last_verified_at < ?
		ORDER BY last_verified_at
		LIMIT ?`, cutoff.UnixNano(), limit)
	if err != nil {
		return nil, fmt.Errorf("store: cari jalur yang belum diverifikasi: %w", err)
	}
	defer rows.Close()

	var out []ObservedFile
	for rows.Next() {
		f, err := scanObservedFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: cari jalur yang belum diverifikasi: %w", err)
	}
	return out, nil
}

// --- helpers ----------------------------------------------------------------

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanVersion(s scanner) (Version, error) {
	var (
		v                      Version
		observedAt, modifiedAt int64
		contentPresent, pinned int
		releasedAt             sql.NullInt64
	)
	err := s.Scan(&v.ID, &v.AssetID, &v.FileHash, &v.Size,
		&observedAt, &modifiedAt, &v.SourcePath,
		&contentPresent, &releasedAt, &pinned)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Version{}, err
		}
		return Version{}, fmt.Errorf("store: baca versi: %w", err)
	}

	v.ObservedAt = time.Unix(0, observedAt)
	v.ModifiedAt = time.Unix(0, modifiedAt)
	v.ContentPresent = contentPresent == 1
	v.Pinned = pinned == 1
	if releasedAt.Valid {
		v.ContentReleasedAt = time.Unix(0, releasedAt.Int64)
	}
	return v, nil
}

func scanObservedFile(s scanner) (ObservedFile, error) {
	var (
		f                                  ObservedFile
		modifiedAt, lastSeenAt, verifiedAt int64
	)
	err := s.Scan(&f.Path, &f.AssetID, &f.LastHash, &f.LastSize,
		&modifiedAt, &lastSeenAt, &verifiedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ObservedFile{}, err
		}
		return ObservedFile{}, fmt.Errorf("store: baca jalur: %w", err)
	}
	f.LastModifiedAt = time.Unix(0, modifiedAt)
	f.LastSeenAt = time.Unix(0, lastSeenAt)
	f.LastVerifiedAt = time.Unix(0, verifiedAt)
	return f, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// escapeLike neutralises the wildcards in a LIKE prefix. Windows paths contain
// no % or _ often, but "C:\foo_bar\" is perfectly legal and would otherwise
// match "C:\fooXbar\".
func escapeLike(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '%', '_', '\\':
			out = append(out, '\\')
		}
		out = append(out, s[i])
	}
	return string(out)
}

func shortHash(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

// --- previews ---------------------------------------------------------------

// PreviewStatus is what came of trying to render a thumbnail.
type PreviewStatus string

const (
	// PreviewOK: a thumbnail exists on disk.
	PreviewOK PreviewStatus = "ok"
	// PreviewUnsupported: the format has no rung on the preview ladder. Not an
	// error — a CapCut project file is a list of references, not a picture.
	PreviewUnsupported PreviewStatus = "unsupported"
	// PreviewFailed: we should have been able to render it and could not. The
	// file may be corrupt or truncated.
	PreviewFailed PreviewStatus = "failed"
)

// Preview records the outcome of one thumbnail attempt, keyed by the content it
// was made from.
type Preview struct {
	FileHash    string
	Status      PreviewStatus
	Source      string
	Width       int
	Height      int
	Bytes       int64
	AttemptedAt time.Time
	Err         string

	// Format is "png" or "jpeg", chosen per image rather than by policy: a flat
	// logo is smaller as PNG, a photograph dramatically smaller as JPEG. Empty
	// when no thumbnail was produced.
	Format string

	// AlphaFlattened is set when the source had transparency that had to be
	// composited onto white to keep the thumbnail within its size budget.
	//
	// The interface must show this. A white logo on transparency, flattened
	// onto white, becomes an empty rectangle — which on a timeline reads as
	// "this version was blank", a lie about the user's own work.
	AlphaFlattened bool
}

// PutPreview records a thumbnail attempt, replacing any earlier one.
func PutPreview(ctx context.Context, db DBTX, p Preview) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO previews
			(file_hash, status, source, width, height, bytes, attempted_at, error,
			 format, alpha_flattened)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(file_hash) DO UPDATE SET
			status          = excluded.status,
			source          = excluded.source,
			width           = excluded.width,
			height          = excluded.height,
			bytes           = excluded.bytes,
			attempted_at    = excluded.attempted_at,
			error           = excluded.error,
			format          = excluded.format,
			alpha_flattened = excluded.alpha_flattened`,
		p.FileHash, string(p.Status), p.Source, p.Width, p.Height, p.Bytes,
		p.AttemptedAt.UnixNano(), p.Err, p.Format, boolToInt(p.AlphaFlattened))
	if err != nil {
		return fmt.Errorf("store: catat pratinjau %s: %w", shortHash(p.FileHash), err)
	}
	return nil
}

// PreviewByFileHash reads one recorded attempt.
func PreviewByFileHash(ctx context.Context, db DBTX, fileHash string) (Preview, error) {
	row := db.QueryRowContext(ctx, `
		SELECT file_hash, status, source, width, height, bytes, attempted_at, error,
		       format, alpha_flattened
		FROM previews WHERE file_hash = ?`, fileHash)

	p, err := scanPreview(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Preview{}, fmt.Errorf("%w: pratinjau %s", ErrNotFound, shortHash(fileHash))
	}
	return p, err
}

// PreviewsForAsset returns the preview attempt for every version of an asset,
// oldest version first, so a timeline can be drawn in one query.
//
// Versions whose content has been thinned away are included: their thumbnail
// outlives the content precisely so the timeline has something to show.
func PreviewsForAsset(ctx context.Context, db DBTX, assetID int64) ([]Preview, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT p.file_hash, p.status, p.source, p.width, p.height, p.bytes,
		       p.attempted_at, p.error, p.format, p.alpha_flattened
		FROM versions v
		JOIN previews p ON p.file_hash = v.file_hash
		WHERE v.asset_id = ?
		ORDER BY v.observed_at, v.id`, assetID)
	if err != nil {
		return nil, fmt.Errorf("store: baca pratinjau karya %d: %w", assetID, err)
	}
	defer rows.Close()

	var out []Preview
	for rows.Next() {
		p, err := scanPreview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: baca pratinjau karya %d: %w", assetID, err)
	}
	return out, nil
}

// FileHashesWithoutPreview returns content that has never been through the
// preview generator, oldest version first.
func FileHashesWithoutPreview(ctx context.Context, db DBTX, limit int) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT v.file_hash
		FROM versions v
		LEFT JOIN previews p ON p.file_hash = v.file_hash
		WHERE p.file_hash IS NULL
		ORDER BY v.observed_at, v.id
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: cari isi tanpa pratinjau: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, fmt.Errorf("store: baca hash: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: cari isi tanpa pratinjau: %w", err)
	}
	return out, nil
}

func scanPreview(s scanner) (Preview, error) {
	var (
		p              Preview
		status         string
		attemptedAt    int64
		alphaFlattened int
	)
	err := s.Scan(&p.FileHash, &status, &p.Source, &p.Width, &p.Height, &p.Bytes,
		&attemptedAt, &p.Err, &p.Format, &alphaFlattened)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Preview{}, err
		}
		return Preview{}, fmt.Errorf("store: baca pratinjau: %w", err)
	}
	p.Status = PreviewStatus(status)
	p.AttemptedAt = time.Unix(0, attemptedAt)
	p.AlphaFlattened = alphaFlattened == 1
	return p, nil
}
