package grouping

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/MufuyuMoku/cit/internal/store"
)

// Explanation is why two files did or did not end up as one work.
//
// Grouping is a pile of tuned numbers, and a pile of tuned numbers with no way
// to see inside it is not something anyone can improve. This is that window:
// every input, every component score, and the one sentence version.
type Explanation struct {
	PathA string
	PathB string

	// What the names reduced to once the noise was stripped. Usually the first
	// thing worth looking at when a pairing surprises you.
	NormalizedA string
	NormalizedB string

	// Component scores, each 0 to 1.
	NameScore  float64
	TimeScore  float64
	ImageScore float64

	// HasImage is false when at least one file has no thumbnail, in which case
	// the image weight is redistributed across the other two rather than
	// counted as zero.
	HasImage bool

	// HammingDistance between the two perceptual hashes, or -1 when there is no
	// visual signal.
	HammingDistance int

	// SameFolderTree reports whether the two sit in one folder or one inside
	// the other. It decides whether an identical name is allowed to overrule a
	// mismatched picture.
	SameFolderTree bool

	// Vetoed is set when the pictures are far enough apart to override the
	// names.
	Vetoed bool

	Total     float64
	Threshold float64

	// Decision is the user's manual judgement for this pair, if there is one.
	// It outranks everything above.
	Decision store.Decision

	// Grouped is what actually happens to this pair.
	Grouped bool

	// Reason is the short human-readable version, in the user's language.
	Reason string
}

// Explain scores one pair of tracked files and reports how it got there.
//
// It changes nothing: this is a question, not an instruction.
func (g *Grouper) Explain(ctx context.Context, pathA, pathB string) (Explanation, error) {
	if pathA == pathB {
		return Explanation{}, fmt.Errorf("grouping: butuh dua jalur berbeda")
	}

	a, err := g.candidateFor(ctx, pathA)
	if err != nil {
		return Explanation{}, err
	}
	b, err := g.candidateFor(ctx, pathB)
	if err != nil {
		return Explanation{}, err
	}

	s := scorePair(a, b, g.w)

	e := Explanation{
		PathA:           pathA,
		PathB:           pathB,
		NormalizedA:     a.normalized,
		NormalizedB:     b.normalized,
		NameScore:       s.name,
		TimeScore:       s.timeScore,
		ImageScore:      s.image,
		HasImage:        s.hasImage,
		HammingDistance: hammingDistance(a.phash, b.phash),
		SameFolderTree:  sameFolderTree(a.path, b.path),
		Vetoed:          s.vetoed,
		Total:           s.total,
		Threshold:       g.w.Threshold,
	}

	decision, err := store.GroupingDecisionFor(ctx, g.db, pathA, pathB)
	switch {
	case err == nil:
		e.Decision = decision.Decision
	case errors.Is(err, store.ErrNotFound):
		// No manual judgement; the scores decide.
	default:
		return Explanation{}, err
	}

	e.Grouped, e.Reason = verdict(e)
	return e, nil
}

// candidateFor builds the scorer's view of one tracked file.
func (g *Grouper) candidateFor(ctx context.Context, path string) (candidate, error) {
	f, err := store.ObservedFileByPath(ctx, g.db, path)
	if errors.Is(err, store.ErrNotFound) {
		return candidate{}, fmt.Errorf("%w: %s", ErrUnknownPath, path)
	}
	if err != nil {
		return candidate{}, err
	}

	phash := ""
	if f.LastHash != "" {
		phash, err = store.PreviewPHash(ctx, g.db, f.LastHash)
		if err != nil {
			return candidate{}, err
		}
	}

	return candidate{
		path:       f.Path,
		assetID:    f.AssetID,
		normalized: normalizeName(f.Path),
		modifiedAt: f.LastModifiedAt,
		phash:      phash,
	}, nil
}

// verdict turns the numbers into the outcome and a sentence saying why.
func verdict(e Explanation) (bool, string) {
	switch e.Decision {
	case store.Together:
		return true, "digabungkan karena kamu yang menyatukannya; skor tidak dipakai"
	case store.Apart:
		return false, "dipisahkan karena kamu yang memisahkannya; skor tidak dipakai"
	}

	if e.Vetoed {
		return false, fmt.Sprintf(
			"gambarnya terlalu jauh berbeda (jarak %d) untuk mempercayai kemiripan namanya%s",
			e.HammingDistance, folderNote(e))
	}

	if e.Total >= e.Threshold {
		return true, fmt.Sprintf("skor %.2f di atas ambang %.2f (%s)",
			e.Total, e.Threshold, components(e))
	}
	return false, fmt.Sprintf("skor %.2f di bawah ambang %.2f (%s)",
		e.Total, e.Threshold, components(e))
}

// folderNote explains why the identical-name exception did not save a pair.
func folderNote(e Explanation) string {
	if e.NormalizedA != e.NormalizedB {
		return ""
	}
	if e.SameFolderTree {
		return ""
	}
	return fmt.Sprintf("; namanya sama (%q) tapi foldernya tidak berhubungan (%s dan %s)",
		e.NormalizedA, filepath.Dir(e.PathA), filepath.Dir(e.PathB))
}

func components(e Explanation) string {
	parts := []string{
		fmt.Sprintf("nama %.2f", e.NameScore),
		fmt.Sprintf("waktu %.2f", e.TimeScore),
	}
	if e.HasImage {
		parts = append(parts, fmt.Sprintf("gambar %.2f, jarak %d", e.ImageScore, e.HammingDistance))
	} else {
		parts = append(parts, "tanpa gambar kecil, bobotnya dibagi ke nama dan waktu")
	}
	return strings.Join(parts, "; ")
}

// String renders an explanation for a log or a terminal.
func (e Explanation) String() string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s\n  vs %s\n", e.PathA, e.PathB)
	fmt.Fprintf(&b, "  nama ternormalisasi : %q / %q\n", e.NormalizedA, e.NormalizedB)
	fmt.Fprintf(&b, "  satu pohon folder   : %v\n", e.SameFolderTree)
	fmt.Fprintf(&b, "  skor nama           : %.3f\n", e.NameScore)
	fmt.Fprintf(&b, "  skor waktu          : %.3f\n", e.TimeScore)
	if e.HasImage {
		fmt.Fprintf(&b, "  skor gambar         : %.3f (jarak hamming %d)\n",
			e.ImageScore, e.HammingDistance)
	} else {
		fmt.Fprintf(&b, "  skor gambar         : tidak ada gambar kecil\n")
	}
	fmt.Fprintf(&b, "  diveto gambar       : %v\n", e.Vetoed)
	fmt.Fprintf(&b, "  total               : %.3f (ambang %.2f)\n", e.Total, e.Threshold)
	if e.Decision != "" {
		fmt.Fprintf(&b, "  keputusan manual    : %s\n", e.Decision)
	}
	fmt.Fprintf(&b, "  hasil               : %s\n", groupedWord(e.Grouped))
	fmt.Fprintf(&b, "  alasan              : %s\n", e.Reason)
	return b.String()
}

func groupedWord(grouped bool) string {
	if grouped {
		return "satu karya"
	}
	return "karya terpisah"
}
