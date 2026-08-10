package lookup

import "time"

// FlagColumn names the images column carrying the operator's per-image
// opt-in for a backend. The two are separate so the per-image choice can
// mirror the per-phase one in Settings: the PTR is free, the boorus spend a
// quota, and an image can reasonably be worth one and not the other.
func FlagColumn(backend string) string {
	if backend == BackendPTR {
		return "scheduled_lookup_ptr"
	}
	return "scheduled_lookup"
}

// CandidateClause is the "worth looking up on this backend" predicate over
// the image alias `i`, shared by both scheduler phases, the Maintenance
// button, and the `lookup:due` filter.
//
// The source test is "no origin carrying a URL", not a bare NOT EXISTS: a PTR
// hit writes a url-less `ptr` origin, and the stricter reading would let one
// PTR match permanently exclude an image from ever finding a real booru
// source. An archive is excluded outright - its own hash is a hash no booru
// and no repository indexes, the pages inside are - and a row whose bytes are
// gone has nothing to hash.
func CandidateClause(backend string) string {
	return `i.is_missing = 0
	  AND i.` + FlagColumn(backend) + ` = 1
	  AND i.file_type <> 'cbz'
	  AND NOT EXISTS (SELECT 1 FROM image_sources s WHERE s.image_id = i.id AND s.url <> '')`
}

// DueClause is the per-backend retry gate over the image alias `i`: true when
// the backend has never been tried, or when its last attempt concluded, its
// delay has passed, and - for the PTR - monloader's index has actually moved
// since the miss was recorded.
//
// The PTR needs both gates. A cursor test alone re-pulls every unmatched
// image on any day the index moved, which for a synced repository is most
// days; a delay alone spends a pass on an index where the answer provably
// cannot differ.
func DueClause(backend string, now time.Time) (string, []any) {
	blocked := `l.queued_at IS NOT NULL OR l.next_due_at IS NULL OR l.next_due_at > ?`
	args := []any{backend, stamp(now)}
	if backend == BackendPTR {
		// A cold cursor cache (no probe has landed) drops the index half
		// rather than blocking every row: over-counting is recoverable,
		// never looking again is not.
		if cursor := PTRCursor.Load(); cursor > 0 {
			blocked += ` OR l.ptr_cursor >= ?`
			args = append(args, int64(cursor))
		}
	}
	return `NOT EXISTS (SELECT 1 FROM image_lookups l
	          WHERE l.image_id = i.id AND l.backend = ? AND (` + blocked + `))`, args
}
