// Package preview builds the tiered thumbnails: direct decoding for png, jpg,
// gif and webp; mergedimage.png out of the kra zip; the flattened composite
// embedded in psd; one ffmpeg frame for mp4 and friends; and a generic icon for
// everything else.
//
// # Where thumbnails live, and why not in the vault
//
// Thumbnails are stored in their own directory tree, addressed by the file hash
// of the content they depict, outside the vault entirely.
//
// The vault was the obvious alternative and it is the wrong answer here.
// Retention in M7 will discard the chunks behind old versions, while the
// invariant says the timeline must never have a hole: a version whose content
// has been thinned away still appears, marked, and it still needs its picture.
// If thumbnails were vault content, keeping them would depend on retention
// remembering never to release their hashes. That is a rule somebody has to
// hold in their head, and the failure mode when they don't is a timeline that
// silently loses its pictures — discovered months later, unrecoverable.
//
// Outside the vault, retention structurally cannot reach them. GC only ever
// deletes chunks whose refcount is zero, and a thumbnail has no chunk, no
// refcount and no row in that table. The invariant is held by the shape of the
// system rather than by discipline, which is the same reason versions_are_
// permanent is a database trigger and not a code review comment.
//
// The consequences, which are real:
//
//   - Sync, whenever it gets built, must carry thumbnails explicitly. Comparing
//     two lists of chunk hashes will not move them, and the receiving side
//     cannot regenerate the ones whose originals have already been thinned. A
//     bundle that omits them arrives with holes in its timeline.
//   - There is no chunk-level deduplication of thumbnails. This costs almost
//     nothing: they are tens of kilobytes and below the vault's 256 KiB minimum
//     chunk size anyway, so chunking would never have shared a byte between two
//     of them. Identical content still shares one thumbnail, because the file
//     hash is the key.
//   - The directory is not a cache and must not be treated as one. A thumbnail
//     can be regenerated only while the original content still exists; once
//     retention has thinned that away, deleting the thumbnail destroys the only
//     surviving picture of that version. It is durable data that happens to be
//     derived.
//
// # Format is chosen per image, not by policy
//
// Measured on 512px thumbnails: a photograph is about eight times smaller as
// JPEG, a screenshot nineteen times smaller as PNG, a flat logo three times
// smaller as PNG. There is no single right answer, so:
//
//   - Transparent, and PNG fits the budget: PNG, transparency kept.
//   - Transparent, and PNG does not fit: flattened onto white as JPEG, with
//     AlphaFlattened recorded so the interface can say what was lost.
//   - Opaque: both are encoded and the smaller wins.
//
// The budget exists for one shape of file: a photographic image with an alpha
// channel, such as a product render on a transparent background, which runs
// past 500 KB as PNG. Those would accumulate quietly in a directory nobody is
// allowed to delete.
//
// Flattening is recorded rather than silent because of what it does to a white
// logo on transparency: composited onto white it becomes an empty rectangle,
// and an empty rectangle on a timeline reads as "this version was blank" — a
// lie about the user's own work. The interface must be able to mark it.
//
// # Untrusted input
//
// Every file read here is hostile until proven otherwise. A psd can be
// truncated mid-structure, a kra can be a zip bomb, a png header can claim
// dimensions that would exhaust memory before a pixel is decoded, and an
// extension is a claim rather than a fact. None of that may crash the
// application, exhaust its memory, or hang it.
//
// The defences, each of which has a test built around a deliberately broken
// file:
//
//   - Dimensions are checked from the header, via DecodeConfig, before any
//     decode allocates.
//   - Decoders run behind a recover, because a panic over one bad file must not
//     take the process with it.
//   - Reads from archives are bounded while reading, not by the size the
//     archive claims.
//   - ffmpeg runs with a deadline and is killed when it expires.
//
// # Failures are recorded, never raised
//
// Generate returns an error only when the database write fails. Everything that
// can go wrong with the file itself becomes a recorded outcome — unsupported or
// failed, with the reason — and the caller carries on. No file may fail to be
// versioned because its picture could not be drawn, and no format is ever
// rejected on the way in.
package preview
