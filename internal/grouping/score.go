package grouping

import (
	"path/filepath"
	"strings"
	"time"
)

// Weights and thresholds for combining the three signals.
//
// The numbers are judgement calls, chosen so that the name carries the decision
// and the other two adjust it. A file's name is what the person typed on
// purpose; save time and pixel similarity are circumstantial.
const (
	// DefaultNameWeight dominates: two files called the same thing after the
	// noise is stripped are almost always the same work.
	DefaultNameWeight = 0.70

	// DefaultTimeWeight is small. Working on two different pieces in one
	// afternoon is completely normal, so closeness in time supports a guess but
	// must never make one.
	DefaultTimeWeight = 0.10

	// DefaultImageWeight sits between them. A matching picture is strong
	// evidence, but plenty of legitimate revisions look nothing like each other.
	DefaultImageWeight = 0.20

	// DefaultThreshold is the combined score at which two files are grouped.
	DefaultThreshold = 0.62

	// DefaultTimeWindow is how far apart two saves can be before closeness in
	// time stops meaning anything. One working day.
	DefaultTimeWindow = 8 * time.Hour

	// DefaultSimilarImageDistance is the Hamming distance below which two
	// thumbnails count as the same picture.
	DefaultSimilarImageDistance = 10

	// DefaultDifferentImageDistance is the distance above which two thumbnails
	// are so unalike that they veto a name match. Chosen well clear of the
	// similar threshold so the middle ground stays undecided rather than
	// swinging on noise.
	DefaultDifferentImageDistance = 26
)

// Weights configures the scorer.
type Weights struct {
	Name  float64
	Time  float64
	Image float64

	Threshold      float64
	TimeWindow     time.Duration
	SimilarImage   int
	DifferentImage int
}

// DefaultWeights returns the tuned defaults.
func DefaultWeights() Weights {
	return Weights{
		Name:           DefaultNameWeight,
		Time:           DefaultTimeWeight,
		Image:          DefaultImageWeight,
		Threshold:      DefaultThreshold,
		TimeWindow:     DefaultTimeWindow,
		SimilarImage:   DefaultSimilarImageDistance,
		DifferentImage: DefaultDifferentImageDistance,
	}
}

// candidate is one tracked file as the scorer sees it.
type candidate struct {
	path       string
	assetID    int64
	normalized string
	modifiedAt time.Time
	phash      string // empty when there is no thumbnail
}

// score is the verdict on one pair.
type score struct {
	total     float64
	name      float64
	timeScore float64
	image     float64

	// vetoed is set when the pictures are so different that a name match cannot
	// be trusted.
	vetoed bool

	// hasImage is false when either file has no thumbnail, in which case the
	// verdict rests on name and time alone.
	hasImage bool
}

// scorePair combines the three signals for two files.
//
// The image signal can veto a name match, with one exception: two files that
// normalise to the same name *and sit in the same folder tree* are the same
// work even when one is a radical revision that looks nothing like the other —
// that is what a revision is.
//
// The folder half of that condition is not a detail. logo_v2.png is the most
// ordinary filename there is, and a freelancer has one in every client folder.
// Without it, the identical-name rule switches off the veto exactly where it is
// needed most, and two clients' histories are silently scrambled into one
// asset.
func scorePair(a, b candidate, w Weights) score {
	var s score

	s.name = nameSimilarity(a.normalized, b.normalized)
	s.timeScore = timeCloseness(a.modifiedAt, b.modifiedAt, w.TimeWindow)

	distance := hammingDistance(a.phash, b.phash)
	s.hasImage = distance >= 0

	switch {
	case !s.hasImage:
		// No picture for at least one of them: an unknown format, or a video
		// with no ffmpeg installed. Those files must still be groupable, so the
		// remaining weight is redistributed rather than counted as zero — a
		// missing signal is not evidence of difference.
		total := w.Name + w.Time
		s.total = (w.Name*s.name + w.Time*s.timeScore) / total

	default:
		s.image = imageCloseness(distance, w.SimilarImage, w.DifferentImage)
		s.total = w.Name*s.name + w.Time*s.timeScore + w.Image*s.image

		if distance > w.DifferentImage && !identicalNameInSameTree(a, b) {
			s.vetoed = true
			s.total = 0
		}
	}

	return s
}

// identicalNameInSameTree reports whether two files carry the same normalised
// name and live close enough together for that to mean anything.
//
// "Close enough" is the same folder, or one inside the other: work genuinely
// does get dragged into an arsip/ subfolder, and that must not break the group.
// Two sibling client folders are not close enough — they are exactly the case
// this exists to separate.
func identicalNameInSameTree(a, b candidate) bool {
	return a.normalized == b.normalized && sameFolderTree(a.path, b.path)
}

// sameFolderTree reports whether two files share a directory, or one's
// directory contains the other's.
func sameFolderTree(a, b string) bool {
	da := filepath.Dir(filepath.Clean(a))
	db := filepath.Dir(filepath.Clean(b))
	if da == db {
		return true
	}
	return within(da, db) || within(db, da)
}

// within reports whether child sits inside parent.
//
// Compared segment by segment through filepath.Rel rather than by string
// prefix, because "proyek" is a prefix of "proyek-lain" without containing it.
func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// timeCloseness decays linearly from 1 at the same instant to 0 a window apart.
func timeCloseness(a, b time.Time, window time.Duration) float64 {
	if window <= 0 {
		return 0
	}
	gap := a.Sub(b)
	if gap < 0 {
		gap = -gap
	}
	if gap >= window {
		return 0
	}
	return 1 - float64(gap)/float64(window)
}

// imageCloseness maps Hamming distance onto 0..1, flat at the ends.
func imageCloseness(distance, similar, different int) float64 {
	switch {
	case distance <= similar:
		return 1
	case distance >= different:
		return 0
	default:
		span := float64(different - similar)
		return 1 - float64(distance-similar)/span
	}
}
