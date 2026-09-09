// Package grouping guesses which tracked files are really one work.
//
// Three signals, all of them plain logic. No model, no training, no dependency
// that could go stale — everything here is regular expressions, edit distance,
// arithmetic on pixels, and subtraction of timestamps.
//
//  1. Name similarity, after stripping the words people add to mean "this is
//     the newer one" — final, fix, ok, revisi 3, v2, dates, stray separators —
//     in Indonesian and English together, because a single filename mixes them
//     constantly.
//  2. Save-time closeness, because files written near each other tend to belong
//     to one working session. Weighted lightly: working on two different pieces
//     in an afternoon is completely normal.
//  3. A 64-bit perceptual hash of the thumbnail: shrink to 8x8, greyscale,
//     compare each cell against the mean. Small Hamming distance means the
//     pictures look alike.
//
// # The hash comes from the thumbnail, never the original
//
// Retention will one day discard the chunks behind old versions. Reading the
// original file to hash it would therefore stop working exactly when a history
// has grown long enough for grouping to matter. Thumbnails live outside the
// vault and outlive the content by design, so a hash derived from them does
// too.
//
// # Manual decisions are permanent
//
// This is the constraint the package is built around. The golden rule is that
// the system observes and proposes while the human decides — so a guess that
// reasserts itself on the next scan is not a proposal, it is an argument the
// user cannot win.
//
// Every Split and Merge is recorded in grouping_decisions and outranks every
// score, for ever. Transitivity is checked too: if the user pulled A and B
// apart, no chain of A-C, C-B similarities may quietly reunite them. Before two
// clusters are joined, every pair across the boundary is checked against the
// recorded decisions.
//
// # What splitting and merging may not break
//
// Tickets attach to versions, and versions attach to assets. Moving a file
// between assets must therefore move its versions with it, or a timeline loses
// entries and the tickets M6 will hang off them point at the wrong work.
//
// Versions are matched by file_key, not source_path. source_path is a
// historical record of where a version was observed and must stay one; file_key
// follows the file through renames. Ingest updates both file_key and the
// recorded grouping decisions when it recognises a rename, so neither the
// history nor the user's judgement is lost when a file is given a better name.
//
// # Files without a thumbnail still group
//
// An unrecognised format, or a video on a machine with no ffmpeg, has no
// picture to hash. Those files fall back to name and time, with the image
// weight redistributed rather than counted as zero: a missing signal is not
// evidence of difference, and a file must never drop out of the system for
// lacking one.
package grouping
