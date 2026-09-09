package ingest

// openState is what a pre-flight open told us about a file.
//
// The distinction between locked and unknown is the whole point. Treating every
// failed open as "still being written" would mean a file on a disconnected
// network drive, or one with permissions we cannot satisfy, waits forever and
// never reaches the timeline — trading one silent failure for another.
type openState int

const (
	// openFree: nobody else holds the file. Safe to read.
	openFree openState = iota

	// openLocked: another process holds it open. This is a writer mid-save, so
	// waiting is the correct response and there is no retry limit — a video
	// export legitimately holds its output for an hour.
	openLocked

	// openUnknown: the open failed for some other reason. Waiting will probably
	// not help, so these are retried a bounded number of times and then
	// surfaced as a problem the user can see.
	openUnknown
)

func (s openState) String() string {
	switch s {
	case openFree:
		return "bebas"
	case openLocked:
		return "terkunci penulis"
	default:
		return "tidak bisa dibuka"
	}
}
