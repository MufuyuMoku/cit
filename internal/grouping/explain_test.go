package grouping

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MufuyuMoku/cit/internal/store"
)

// The case that started this: two clients, same filename, different logos.
// Explain has to make it obvious at a glance why they stayed apart.
func TestExplainTwoClientLogos(t *testing.T) {
	f := newFixture(t)

	a := f.addFileAt(filepath.Join("klien-warung-kopi", "logo_v2.png"), baseTime, artwork(1))
	b := f.addFileAt(filepath.Join("klien-bengkel-motor", "logo_v2.png"),
		baseTime.Add(20*time.Minute), artwork(9))

	f.regroup()

	e, err := f.g.Explain(t.Context(), a, b)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	t.Logf("\n%s", e)

	if e.Grouped {
		t.Error("Grouped = true; keduanya harusnya terpisah")
	}
	if !e.Vetoed {
		t.Error("Vetoed = false; pemisahnya seharusnya veto gambar")
	}
	if e.NormalizedA != e.NormalizedB {
		t.Errorf("nama ternormalisasi %q dan %q; uji ini mengandaikan keduanya sama",
			e.NormalizedA, e.NormalizedB)
	}
	if e.SameFolderTree {
		t.Error("SameFolderTree = true untuk dua folder klien berbeda")
	}
	if e.HammingDistance <= DefaultDifferentImageDistance {
		t.Errorf("jarak %d tidak di atas ambang veto %d",
			e.HammingDistance, DefaultDifferentImageDistance)
	}

	// The sentence has to name the actual cause, not just say no.
	for _, want := range []string{"gambarnya terlalu jauh berbeda", "foldernya tidak berhubungan"} {
		if !strings.Contains(e.Reason, want) {
			t.Errorf("alasan tidak menyebut %q:\n  %s", want, e.Reason)
		}
	}
}

func TestExplainAGroupedPair(t *testing.T) {
	f := newFixture(t)

	a := f.addFileAt(filepath.Join("proyek", "poster-kampus.psd"), baseTime, nearlyIdentical(3))
	b := f.addFileAt(filepath.Join("proyek", "poster kampus fix.psd"),
		baseTime.Add(40*time.Minute), nearlyIdentical(3))

	f.regroup()

	e, err := f.g.Explain(t.Context(), a, b)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	t.Logf("\n%s", e)

	if !e.Grouped {
		t.Errorf("Grouped = false: %s", e.Reason)
	}
	if e.Total < e.Threshold {
		t.Errorf("total %.3f di bawah ambang %.2f tapi tetap dikelompokkan", e.Total, e.Threshold)
	}
	if e.NameScore != 1 {
		t.Errorf("skor nama %.3f, mau 1 untuk nama yang ternormalisasi sama", e.NameScore)
	}
	if !e.HasImage {
		t.Error("HasImage = false padahal keduanya punya gambar kecil")
	}
	if !strings.Contains(e.Reason, "di atas ambang") {
		t.Errorf("alasan = %q", e.Reason)
	}
}

// A manual decision outranks the scores, and Explain has to say so rather than
// reporting numbers that had no effect.
func TestExplainReportsManualDecisions(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	a := f.addFileAt(filepath.Join("proyek", "banner.png"), baseTime, nearlyIdentical(4))
	b := f.addFileAt(filepath.Join("proyek", "banner fix.png"), baseTime.Add(time.Minute), nearlyIdentical(4))

	f.regroup()

	if err := f.g.Split(ctx, a, b); err != nil {
		t.Fatalf("Split: %v", err)
	}
	e, err := f.g.Explain(ctx, a, b)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	t.Logf("\n%s", e)

	if e.Decision != store.Apart {
		t.Errorf("Decision = %q, mau apart", e.Decision)
	}
	if e.Grouped {
		t.Error("Grouped = true meski pengguna memisahkannya")
	}
	if !strings.Contains(e.Reason, "kamu yang memisahkannya") {
		t.Errorf("alasan tidak menyebut keputusan pengguna: %q", e.Reason)
	}
	// The scores are still reported, so the user can see what the system would
	// have done on its own.
	if e.Total == 0 && e.NameScore == 0 {
		t.Error("skor tidak dilaporkan sama sekali")
	}

	if err := f.g.Merge(ctx, a, b); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	e, _ = f.g.Explain(ctx, a, b)
	if e.Decision != store.Together || !e.Grouped {
		t.Errorf("setelah digabung manual: Decision=%q Grouped=%v", e.Decision, e.Grouped)
	}
	if !strings.Contains(e.Reason, "kamu yang menyatukannya") {
		t.Errorf("alasan = %q", e.Reason)
	}
}

// Files with no thumbnail must be explainable too, and the explanation has to
// say that the image weight was redistributed rather than scored zero.
func TestExplainWithoutThumbnails(t *testing.T) {
	f := newFixture(t)

	a := f.addFileAt(filepath.Join("video", "opening.mp4"), baseTime, nil)
	b := f.addFileAt(filepath.Join("video", "opening fix.mp4"), baseTime.Add(30*time.Minute), nil)

	f.regroup()

	e, err := f.g.Explain(t.Context(), a, b)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	t.Logf("\n%s", e)

	if e.HasImage {
		t.Error("HasImage = true padahal tidak ada gambar kecil")
	}
	if e.HammingDistance != -1 {
		t.Errorf("HammingDistance = %d, mau -1 ketika tidak ada sinyal gambar", e.HammingDistance)
	}
	if !strings.Contains(e.Reason, "tanpa gambar kecil") {
		t.Errorf("alasan tidak menyebutkan hilangnya sinyal gambar: %q", e.Reason)
	}
	if !e.Grouped {
		t.Errorf("Grouped = false: %s", e.Reason)
	}
}

func TestExplainRejectsUnknownAndIdenticalPaths(t *testing.T) {
	f := newFixture(t)
	real := f.addFile("ada.png", baseTime, artwork(1))

	if _, err := f.g.Explain(t.Context(), real, f.path("tidak-ada.png")); !errors.Is(err, ErrUnknownPath) {
		t.Errorf("jalur tak dikenal = %v, mau ErrUnknownPath", err)
	}
	if _, err := f.g.Explain(t.Context(), real, real); err == nil {
		t.Error("jalur yang sama dua kali diterima")
	}
}

// Explain must not change anything.
func TestExplainChangesNothing(t *testing.T) {
	f := newFixture(t)

	a := f.addFileAt(filepath.Join("proyek", "a.png"), baseTime, artwork(1))
	b := f.addFileAt(filepath.Join("proyek", "b.png"), baseTime, artwork(9))

	f.regroup()
	assetA, assetB := f.assetOf(a), f.assetOf(b)
	versions := f.versionCount()

	for i := 0; i < 5; i++ {
		if _, err := f.g.Explain(t.Context(), a, b); err != nil {
			t.Fatalf("Explain: %v", err)
		}
	}

	if f.assetOf(a) != assetA || f.assetOf(b) != assetB {
		t.Error("Explain memindahkan berkas antar karya")
	}
	if f.versionCount() != versions {
		t.Error("Explain mengubah jumlah versi")
	}
	if _, err := store.GroupingDecisionFor(t.Context(), f.db, a, b); !errors.Is(err, store.ErrNotFound) {
		t.Error("Explain mencatat keputusan")
	}
}
