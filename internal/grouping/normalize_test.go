package grouping

import "testing"

// The names here are the point of the whole file. They are what design work
// actually looks like on disk: Indonesian and English noise words in the same
// filename, version markers in three notations, dates in two, and separators
// used interchangeably.
func TestNormalizeStripsTheNoiseRealFilenamesCarry(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		// The seven-file poster case, every one reducing to the same thing.
		{"poster-kampus.psd", "poster kampus"},
		{"poster kampus final.psd", "poster kampus"},
		{"poster_kampus_fix.psd", "poster kampus"},
		{"poster kampus revisi 3.psd", "poster kampus"},
		{"Poster Kampus FINAL ASLI.psd", "poster kampus"},
		{"poster_kampus_v2 (1).psd", "poster kampus"},
		{"poster-kampus-20260909.psd", "poster kampus"},

		// Version markers in the notations people use.
		{"logo v2.png", "logo"},
		{"logo_v10.png", "logo"},
		{"logo-ver3.png", "logo"},
		{"logo versi 4.png", "logo"},
		{"logo rev2.png", "logo"},
		{"logo_revisi_7.png", "logo"},

		// Mixed languages inside one name, which is the normal case.
		{"brosur fix final banget.psd", "brosur"},
		{"brosur_final_fix_ok.psd", "brosur"},
		{"undangan revisi 2 FIX ASLI.psd", "undangan"},
		{"kartu nama new terbaru.ai", "kartu nama"},
		{"spanduk done selesai.psd", "spanduk"},

		// Dates in the shapes people type.
		{"feed 2026-09-09.png", "feed"},
		{"feed_09-09-2026.png", "feed"},
		{"feed 20260909.png", "feed"},
		{"feed-260909.png", "feed"},

		// Operating-system copy counters.
		{"katalog (1).psd", "katalog"},
		{"katalog (12).psd", "katalog"},
		{"katalog - Copy.psd", "katalog"},
		{"salinan katalog.psd", "katalog"},

		// Separators used interchangeably, and casing.
		{"MENU___MAKANAN.psd", "menu makanan"},
		{"menu---makanan.psd", "menu makanan"},
		{"Menu.Makanan.psd", "menu makanan"},
		{"  menu  makanan  .psd", "menu makanan"},

		// Bare numbers are kept: "logo 2" may well be a second, different logo,
		// and wrongly merging two works scrambles two histories together.
		{"logo 2.png", "logo 2"},
		{"logo-3.png", "logo 3"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeName("/kerja/" + tc.name); got != tc.want {
				t.Errorf("normalizeName(%q) = %q, mau %q", tc.name, got, tc.want)
			}
		})
	}
}

// A name made entirely of noise words must not collapse to nothing, or every
// such file in a folder would be merged into one.
func TestNamesThatAreAllNoiseKeepTheirIdentity(t *testing.T) {
	a := normalizeName("/kerja/final.psd")
	b := normalizeName("/kerja/fix.psd")
	c := normalizeName("/kerja/revisi 2.psd")

	for _, n := range []string{a, b, c} {
		if n == "" {
			t.Fatal("nama yang seluruhnya kata pengisi jadi kosong")
		}
	}
	if a == b || b == c || a == c {
		t.Errorf("nama berbeda jadi sama: %q, %q, %q", a, b, c)
	}
}

func TestNameSimilarity(t *testing.T) {
	cases := []struct {
		a, b string
		min  float64
		max  float64
	}{
		{"poster kampus", "poster kampus", 1, 1},
		{"poster kampus", "poster kampuss", 0.85, 0.99},
		{"logo warung", "logo warung 2", 0.80, 0.95},
		{"poster kampus", "logo kopi", 0, 0.45},
		{"", "apa saja", 0, 0},
	}

	for _, tc := range cases {
		got := nameSimilarity(tc.a, tc.b)
		if got < tc.min || got > tc.max {
			t.Errorf("nameSimilarity(%q, %q) = %.3f, mau antara %.2f dan %.2f",
				tc.a, tc.b, got, tc.min, tc.max)
		}
	}
}

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"abc", "", 3},
		{"kitten", "sitting", 3},
		{"poster", "poster2", 1},
	}
	for _, tc := range cases {
		if got := levenshtein(tc.a, tc.b); got != tc.want {
			t.Errorf("levenshtein(%q, %q) = %d, mau %d", tc.a, tc.b, got, tc.want)
		}
	}
}
