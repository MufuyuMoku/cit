package grouping

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Filenames are the strongest signal available, and in this domain they are
// also the messiest. Real files from real design work look like:
//
//	poster-final.psd
//	poster_final_fix.psd
//	poster fix banget.psd
//	poster revisi 3.psd
//	Poster FINAL ASLI.psd
//	poster_v2 (1).psd
//	poster-20260909.psd
//	salinan poster - Copy.psd
//
// All eight are one work. Normalising means stripping the words and markers
// people add to say "this is the newer one", in both languages, because they
// are mixed within a single filename constantly — "poster fix final banget" is
// not a joke, it is Tuesday.

// noiseWords are tokens that say "this is a later or better take" and carry no
// information about which work the file is.
//
// English and Indonesian together, deliberately: a designer typing quickly
// reaches for whichever word is shorter, and both appear in the same name.
var noiseWords = map[string]bool{
	// English
	"final": true, "finals": true, "fix": true, "fixed": true, "fixes": true,
	"ok": true, "okay": true, "done": true, "new": true, "latest": true,
	"last": true, "draft": true, "sketch": true, "copy": true, "edit": true,
	"edited": true, "revised": true, "revision": true, "clean": true,
	"use": true, "used": true, "real": true, "print": true, "web": true,

	// Indonesian
	"oke": true, "sip": true, "selesai": true, "jadi": true, "asli": true,
	"baru": true, "terbaru": true, "akhir": true, "banget": true, "bgt": true,
	"beneran": true, "sketsa": true, "salinan": true, "coba": true,
	"percobaan": true, "revisi": true, "perbaikan": true, "benerin": true,
	"pakai": true, "dipakai": true, "cetak": true, "kirim": true,
	"bener": true, "fixx": true, "fiks": true,
}

var (
	// Version and revision markers with a number attached: v2, rev3, revisi 4,
	// ver_5. The number is part of the marker, not part of the name.
	versionMarker = regexp.MustCompile(`(?i)\b(?:v|ver|versi|rev|revisi|r)\s*[-_.]?\s*\d+\b`)

	// A bare "copy" counter from the operating system: "(1)", "(2)".
	copyCounter = regexp.MustCompile(`\(\s*\d+\s*\)`)

	// Dates in the shapes people actually type. Longest first, because
	// 20260909 must not be eaten as 2026 followed by rubbish.
	datePatterns = []*regexp.Regexp{
		regexp.MustCompile(`\b\d{4}[-_.]\d{2}[-_.]\d{2}\b`),     // 2026-09-09
		regexp.MustCompile(`\b\d{2}[-_.]\d{2}[-_.]\d{4}\b`),     // 09-09-2026
		regexp.MustCompile(`\b\d{8}\b`),                         // 20260909
		regexp.MustCompile(`\b\d{6}\b`),                         // 260909
		regexp.MustCompile(`\b\d{1,2}[-_.]\d{1,2}[-_.]\d{2}\b`), // 9-9-26
	}

	// Time stamps some tools append: 143022, 14-30-22.
	timeStamp = regexp.MustCompile(`\b\d{2}[-_.]\d{2}[-_.]\d{2}\b`)

	// Separator runs collapse to a single space.
	separators = regexp.MustCompile(`[\s_\-.]+`)
)

// normalizeName reduces a filename to the part that identifies the work.
//
// Bare numbers are deliberately kept. "logo 2" may well be a second, different
// logo, and merging two works wrongly scrambles two histories into one, which
// is far more disorienting to undo than leaving them apart. When in doubt, stay
// apart: splitting is a click, unpicking a merged timeline is not.
func normalizeName(path string) string {
	name := filepath.Base(path)
	name = strings.TrimSuffix(name, filepath.Ext(name))
	name = strings.ToLower(name)

	// Underscore is a word character as far as a regexp is concerned, so 
	// never fires beside one: neither the v2 in poster_kampus_v2 nor the date in
	// feed_09-09-2026 can be seen while the underscores are still there. In a
	// filename an underscore means exactly what a dash means, so turn it into
	// one first and every pattern below works.
	name = strings.ReplaceAll(name, "_", "-")

	name = copyCounter.ReplaceAllString(name, " ")

	// Dates before separators are collapsed, because 2026-09-09 needs its
	// dashes to still be recognisable as a date.
	for _, re := range datePatterns {
		name = re.ReplaceAllString(name, " ")
	}
	name = timeStamp.ReplaceAllString(name, " ")

	name = separators.ReplaceAllString(name, " ")
	name = versionMarker.ReplaceAllString(name, " ")

	fields := strings.Fields(name)
	kept := make([]string, 0, len(fields))
	for _, f := range fields {
		if noiseWords[f] {
			continue
		}
		kept = append(kept, f)
	}

	// Everything was noise: "final fix ok.psd". Fall back to the raw stem so
	// two such files do not all collapse into one empty name and get merged.
	if len(kept) == 0 {
		stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		return strings.ToLower(strings.TrimSpace(separators.ReplaceAllString(stem, " ")))
	}
	return strings.Join(kept, " ")
}

// nameSimilarity scores two normalised names from 0 to 1.
//
// Levenshtein over the whole string rather than token overlap: designers append
// and misspell far more often than they reorder, so edit distance matches what
// actually varies between "poster kampus" and "poster kampuss".
func nameSimilarity(a, b string) float64 {
	if a == b {
		return 1
	}
	if a == "" || b == "" {
		return 0
	}

	distance := levenshtein(a, b)
	longest := len(a)
	if len(b) > longest {
		longest = len(b)
	}
	score := 1 - float64(distance)/float64(longest)
	if score < 0 {
		return 0
	}
	return score
}

// levenshtein is the classic two-row edit distance, over bytes.
//
// Bytes rather than runes: filenames here are overwhelmingly ASCII, and a
// multi-byte character counts as a couple of edits instead of one, which only
// makes the score more cautious.
func levenshtein(a, b string) int {
	if len(a) < len(b) {
		a, b = b, a
	}
	if len(b) == 0 {
		return len(a)
	}

	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}
