package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Decision is a grouping judgement the user made by hand.
//
// These outrank every automatic signal, permanently. The golden rule is that
// the system observes and proposes while the human decides; a guess that
// reasserts itself on the next scan is not a proposal but an argument the user
// cannot win.
type Decision string

const (
	// Together: these two files are the same work, whatever the scores say.
	Together Decision = "together"
	// Apart: these two files are different works, whatever the scores say.
	Apart Decision = "apart"
)

// GroupingDecision is one recorded judgement.
type GroupingDecision struct {
	PathA     string
	PathB     string
	Decision  Decision
	DecidedAt time.Time
}

// orderPair puts a path pair in canonical order, so a pair has exactly one row
// however it is asked about.
func orderPair(a, b string) (string, string) {
	if a <= b {
		return a, b
	}
	return b, a
}

// PutGroupingDecision records a manual judgement, replacing any earlier one for
// the same pair.
func PutGroupingDecision(ctx context.Context, db DBTX, a, b string, d Decision, now time.Time) error {
	if a == b {
		return fmt.Errorf("store: keputusan pengelompokan butuh dua jalur berbeda, dapat %q dua kali", a)
	}
	pa, pb := orderPair(a, b)

	_, err := db.ExecContext(ctx, `
		INSERT INTO grouping_decisions (path_a, path_b, decision, decided_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(path_a, path_b) DO UPDATE SET
			decision   = excluded.decision,
			decided_at = excluded.decided_at`,
		pa, pb, string(d), now.UnixNano())
	if err != nil {
		return fmt.Errorf("store: catat keputusan pengelompokan: %w", err)
	}
	return nil
}

// GroupingDecisionFor reads the judgement for one pair, in either order.
func GroupingDecisionFor(ctx context.Context, db DBTX, a, b string) (GroupingDecision, error) {
	pa, pb := orderPair(a, b)

	var (
		g         GroupingDecision
		decision  string
		decidedAt int64
	)
	err := db.QueryRowContext(ctx, `
		SELECT path_a, path_b, decision, decided_at
		FROM grouping_decisions WHERE path_a = ? AND path_b = ?`, pa, pb).
		Scan(&g.PathA, &g.PathB, &decision, &decidedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return GroupingDecision{}, fmt.Errorf("%w: keputusan untuk %s dan %s", ErrNotFound, a, b)
	}
	if err != nil {
		return GroupingDecision{}, fmt.Errorf("store: baca keputusan pengelompokan: %w", err)
	}
	g.Decision = Decision(decision)
	g.DecidedAt = time.Unix(0, decidedAt)
	return g, nil
}

// AllGroupingDecisions returns every manual judgement.
func AllGroupingDecisions(ctx context.Context, db DBTX) ([]GroupingDecision, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT path_a, path_b, decision, decided_at
		FROM grouping_decisions
		ORDER BY path_a, path_b`)
	if err != nil {
		return nil, fmt.Errorf("store: baca keputusan pengelompokan: %w", err)
	}
	defer rows.Close()

	var out []GroupingDecision
	for rows.Next() {
		var (
			g         GroupingDecision
			decision  string
			decidedAt int64
		)
		if err := rows.Scan(&g.PathA, &g.PathB, &decision, &decidedAt); err != nil {
			return nil, fmt.Errorf("store: baca keputusan: %w", err)
		}
		g.Decision = Decision(decision)
		g.DecidedAt = time.Unix(0, decidedAt)
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: baca keputusan pengelompokan: %w", err)
	}
	return out, nil
}

// RenameGroupingDecisions follows a file through a rename, so a judgement the
// user made survives them moving the file.
func RenameGroupingDecisions(ctx context.Context, db DBTX, oldPath, newPath string) error {
	// Rewrite both columns, then re-canonicalise: a pair whose order flipped
	// would otherwise become unreachable.
	for _, column := range []string{"path_a", "path_b"} {
		if _, err := db.ExecContext(ctx,
			fmt.Sprintf(`UPDATE OR IGNORE grouping_decisions SET %s = ? WHERE %s = ?`, column, column),
			newPath, oldPath); err != nil {
			return fmt.Errorf("store: pindahkan keputusan pengelompokan: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE OR IGNORE grouping_decisions
		SET path_a = path_b, path_b = path_a
		WHERE path_a > path_b`); err != nil {
		return fmt.Errorf("store: rapikan urutan keputusan: %w", err)
	}
	// A rename onto itself can leave a degenerate row behind.
	if _, err := db.ExecContext(ctx,
		`DELETE FROM grouping_decisions WHERE path_a = path_b`); err != nil {
		return fmt.Errorf("store: bersihkan keputusan kembar: %w", err)
	}
	return nil
}

// --- perceptual hashes ------------------------------------------------------

// SetPreviewPHash records the perceptual hash of a thumbnail.
func SetPreviewPHash(ctx context.Context, db DBTX, fileHash, phash string) error {
	if _, err := db.ExecContext(ctx,
		`UPDATE previews SET phash = ? WHERE file_hash = ?`, phash, fileHash); err != nil {
		return fmt.Errorf("store: catat phash %s: %w", shortHash(fileHash), err)
	}
	return nil
}

// PreviewPHash reads the perceptual hash for a piece of content. An empty
// string means there is none: no thumbnail, or not hashed yet.
func PreviewPHash(ctx context.Context, db DBTX, fileHash string) (string, error) {
	var phash string
	err := db.QueryRowContext(ctx,
		`SELECT phash FROM previews WHERE file_hash = ?`, fileHash).Scan(&phash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: baca phash %s: %w", shortHash(fileHash), err)
	}
	return phash, nil
}

// --- moving files and versions between assets -------------------------------

// MoveFileToAsset repoints one tracked path and all of its versions at another
// asset.
//
// Versions are matched on file_key rather than source_path: source_path records
// where a version was when it was seen, which stops being the file's location
// the moment it is renamed. Getting this wrong would strand a file's older
// versions on the asset it came from, and the user would see a timeline with
// entries missing.
func MoveFileToAsset(ctx context.Context, db DBTX, path string, assetID int64, now time.Time) error {
	if _, err := db.ExecContext(ctx,
		`UPDATE observed_files SET asset_id = ? WHERE path = ?`, assetID, path); err != nil {
		return fmt.Errorf("store: pindahkan jalur %s ke karya %d: %w", path, assetID, err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE versions SET asset_id = ? WHERE file_key = ?`, assetID, path); err != nil {
		return fmt.Errorf("store: pindahkan versi %s ke karya %d: %w", path, assetID, err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE assets SET updated_at = ? WHERE id = ?`, now.UnixNano(), assetID); err != nil {
		return fmt.Errorf("store: perbarui karya %d: %w", assetID, err)
	}
	return nil
}

// RenameVersionFileKey follows a file through a rename so its versions stay
// attached to it.
func RenameVersionFileKey(ctx context.Context, db DBTX, oldPath, newPath string) error {
	if _, err := db.ExecContext(ctx,
		`UPDATE versions SET file_key = ? WHERE file_key = ?`, newPath, oldPath); err != nil {
		return fmt.Errorf("store: pindahkan kunci berkas versi: %w", err)
	}
	return nil
}

// DeleteEmptyAsset removes an asset that has no versions and no tracked files.
//
// Assets that still hold versions are left alone — the database refuses to drop
// them anyway, because deleting one would take a piece of the user's history
// with it.
func DeleteEmptyAsset(ctx context.Context, db DBTX, assetID int64) (bool, error) {
	var versions, files int
	if err := db.QueryRowContext(ctx,
		`SELECT (SELECT count(*) FROM versions WHERE asset_id = ?),
		        (SELECT count(*) FROM observed_files WHERE asset_id = ?)`,
		assetID, assetID).Scan(&versions, &files); err != nil {
		return false, fmt.Errorf("store: periksa karya %d: %w", assetID, err)
	}
	if versions > 0 || files > 0 {
		return false, nil
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM assets WHERE id = ?`, assetID); err != nil {
		return false, fmt.Errorf("store: hapus karya kosong %d: %w", assetID, err)
	}
	return true, nil
}

// AllObservedFiles returns every tracked path, ordered for reproducibility.
func AllObservedFiles(ctx context.Context, db DBTX) ([]ObservedFile, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT path, asset_id, last_hash, last_size, last_modified_at, last_seen_at, last_verified_at
		FROM observed_files
		ORDER BY path`)
	if err != nil {
		return nil, fmt.Errorf("store: daftar semua jalur: %w", err)
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
		return nil, fmt.Errorf("store: daftar semua jalur: %w", err)
	}
	return out, nil
}
