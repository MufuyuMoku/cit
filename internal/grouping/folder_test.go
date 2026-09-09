package grouping

import (
	"path/filepath"
	"testing"
	"time"
)

// Two clients, two folders, two entirely different logos — and the same
// filename, which is the single most ordinary thing in a freelancer's
// directory tree.
//
// The identical-name rule disables the pHash veto, so if the folder is not part
// of the comparison these merge into one asset and two clients' histories are
// scrambled together.
func TestSameFilenameInUnrelatedFoldersStaysApart(t *testing.T) {
	f := newFixture(t)

	w := DefaultWeights()
	distance := hammingDistance(phashOf(artwork(1)), phashOf(artwork(9)))
	if distance <= w.DifferentImage {
		t.Fatalf("prasyarat gagal: jarak gambar %d, harus di atas ambang veto %d",
			distance, w.DifferentImage)
	}

	a := f.addFileAt(filepath.Join("klien-warung-kopi", "logo_v2.png"), baseTime, artwork(1))
	b := f.addFileAt(filepath.Join("klien-bengkel-motor", "logo_v2.png"),
		baseTime.Add(20*time.Minute), artwork(9))

	f.regroup()

	f.requireDifferentAssets(a, b)
	if got := f.assetCount(); got != 2 {
		t.Errorf("%d karya, mau 2; dua logo klien berbeda menyatu jadi satu riwayat", got)
	}
}

// The same name in the same folder is a different matter: that really is one
// work saved twice.
func TestSameFilenameInOneFolderStillGroups(t *testing.T) {
	f := newFixture(t)

	a := f.addFileAt(filepath.Join("klien-warung-kopi", "logo.png"), baseTime, nearlyIdentical(1))
	b := f.addFileAt(filepath.Join("klien-warung-kopi", "logo_v2.png"),
		baseTime.Add(20*time.Minute), nearlyIdentical(1))

	f.regroup()

	f.requireSameAsset(a, b)
}

// A work genuinely does move between folders sometimes — dragged into an
// "arsip" subfolder, say. That must still group when the pictures agree.
func TestSameWorkMovedToASubfolderStillGroups(t *testing.T) {
	f := newFixture(t)

	a := f.addFileAt(filepath.Join("proyek-a", "brosur.psd"), baseTime, nearlyIdentical(5))
	b := f.addFileAt(filepath.Join("proyek-a", "arsip", "brosur fix.psd"),
		baseTime.Add(time.Hour), nearlyIdentical(5))

	f.regroup()

	f.requireSameAsset(a, b)
}

// The boundary cases sameFolderTree has to get right. A string prefix is not
// containment: "proyek" is a prefix of "proyek-lain" and contains nothing of it.
func TestSameFolderTree(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{filepath.Join("k", "logo.png"), filepath.Join("k", "logo2.png"), true},
		{filepath.Join("k", "a", "logo.png"), filepath.Join("k", "a", "arsip", "logo.png"), true},
		{filepath.Join("k", "a", "arsip", "logo.png"), filepath.Join("k", "a", "logo.png"), true},
		{filepath.Join("k", "a", "x", "y", "logo.png"), filepath.Join("k", "a", "logo.png"), true},

		{filepath.Join("k", "a", "logo.png"), filepath.Join("k", "b", "logo.png"), false},
		{filepath.Join("k", "proyek", "logo.png"), filepath.Join("k", "proyek-lain", "logo.png"), false},
		{filepath.Join("k", "klien-a", "logo.png"), filepath.Join("k", "klien-b", "logo.png"), false},
	}

	for _, tc := range cases {
		if got := sameFolderTree(tc.a, tc.b); got != tc.want {
			t.Errorf("sameFolderTree(%q, %q) = %v, mau %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// A folder whose name merely starts with another folder's name is a different
// folder, and the veto must still apply there.
func TestPrefixNamedFoldersAreNotTheSameTree(t *testing.T) {
	f := newFixture(t)

	a := f.addFileAt(filepath.Join("proyek", "logo.png"), baseTime, artwork(1))
	b := f.addFileAt(filepath.Join("proyek-lain", "logo.png"), baseTime.Add(10*time.Minute), artwork(9))

	f.regroup()

	f.requireDifferentAssets(a, b)
}

// And the exception still does its job: a radical revision in the same folder
// stays one work despite the pictures disagreeing completely.
func TestRadicalRevisionInSameFolderStillGroups(t *testing.T) {
	f := newFixture(t)

	w := DefaultWeights()
	if d := hammingDistance(phashOf(artwork(3)), phashOf(artwork(11))); d <= w.DifferentImage {
		t.Fatalf("prasyarat gagal: jarak gambar %d, harus di atas ambang veto %d", d, w.DifferentImage)
	}

	a := f.addFileAt(filepath.Join("proyek", "poster.psd"), baseTime, artwork(3))
	b := f.addFileAt(filepath.Join("proyek", "poster final.psd"), baseTime.Add(2*time.Hour), artwork(11))

	f.regroup()

	f.requireSameAsset(a, b)
}
