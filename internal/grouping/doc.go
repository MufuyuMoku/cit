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
// The picture does more than contribute a score: far enough apart, it vetoes a
// name match outright, because names collide far more often than images do. That
// veto has one exception — two files that normalise to the same name and sit in
// the same folder tree are one work even when one is a radical revision that
// looks nothing like the other, which is what a revision is.
//
// The folder half of that condition is not a detail. logo_v2.png is the most
// ordinary filename there is, and a freelancer has one in every client folder.
// Without it the identical-name exception switches the veto off exactly where it
// is needed most, and two clients' histories are silently scrambled into one
// asset. Folders are compared segment by segment, not by string prefix: "proyek"
// is a prefix of "proyek-lain" without containing it.
//
// Explain reports the whole breakdown for one pair — every input, every
// component, the veto, the manual decision if there is one — and changes
// nothing. The weights are judgement calls that will need tuning against real
// filenames, and tuning them without being able to see inside is guesswork.
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
// Holding that under concurrency takes one more thing, and it is load-bearing.
// Scoring happens outside any transaction, so there is a window between reading
// the catalogue and writing the conclusions — and a user who split two files by
// hand inside that window used to have the decision undone by arithmetic older
// than the decision itself: the row stayed in grouping_decisions while the two
// files were merged back together. Regroup therefore records
// store.GroupingGeneration before it starts scoring and re-reads it inside the
// transaction that would do the writing. If the number moved, the pass returns
// ErrStale and writes nothing. Remove that check and the promise above is no
// longer true.
//
// # When it runs
//
// Not from inside a scan. Scoring is quadratic in the number of tracked files:
// around 860 ns per pair, so half a second at a thousand files and forty-odd
// seconds at ten thousand, while a watch cycle comes round every second. Calling
// it per scan gives, past a couple of thousand files, a scan that can never catch
// up with itself — and the slowdown arrives gradually as the archive grows, long
// after anyone would think to look for it.
//
// Scheduler runs it beside ingesting instead. A scan that changed something marks
// the catalogue dirty and starts nothing, because someone is evidently still
// working; a scan that changed nothing is the signal that they stopped, and that
// is when a pass starts, on its own goroutine, no more often than
// DefaultMinInterval. Wire it up with ingest.WithAfterScan — the hook passes a
// bool rather than a Result so neither package has to import the other.
//
// One pass runs at a time, and the scoring sweep checks for cancellation once per
// row so closing the window does not appear to hang.
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
