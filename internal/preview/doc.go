// Package preview builds the tiered thumbnails: direct decoding for png and
// jpg, mergedimage.png out of the kra zip, the flattened composite embedded in
// psd, one ffmpeg frame for mp4, and a generic icon for everything else.
//
// ffmpeg is the only external dependency in CIT and it is optional; when it is
// missing from PATH, video previews degrade to the generic icon.
//
// Nothing is implemented yet.
package preview
