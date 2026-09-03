// Package grouping guesses which files are really one asset, using three
// signals and no AI whatsoever: filename similarity after stripping suffixes
// like final/fix/ok/revisi3/v2, write-time proximity, and a 64-bit perceptual
// hash compared by Hamming distance.
//
// Nothing is implemented yet.
package grouping
