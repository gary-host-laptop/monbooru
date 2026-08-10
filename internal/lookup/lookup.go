// Package lookup holds the scheduled hash lookup's retry ladder, the "due"
// predicate, and the per-image attempt history. It is a leaf: the scheduler
// runs the phases, the search where-builder answers `lookup:due`, and the API
// records outcomes, and all three have to agree on what "due" means. Nothing
// here imports internal/web or internal/search, so neither import forms a
// cycle.
package lookup

import (
	"sync/atomic"
	"time"
)

// Backends a lookup can target. A manual "all" lookup writes both rows.
const (
	BackendPTR   = "ptr"
	BackendBooru = "booru"
)

// Concluded outcomes stored in last_result. An attempt still in flight has
// no result at all - queued_at carries that state instead.
const (
	ResultHit   = "hit"
	ResultMiss  = "miss"
	ResultError = "error"
)

// The retry ladders, indexed by the number of consecutive concluded misses.
// Fixed rather than configurable, like the tag-similarity weights: the only
// operator knob is the budget, and it lives on monloader.
//
// The PTR is free per image, so its ladder never gives up - an image the
// repository has never seen keeps missing every few weeks and is eventually
// right when someone contributes its hash. The online ladder spans about 31
// weeks before the sixth miss; an image that has matched no booru in seven
// months across five attempts is in practice not on one, and continuing to
// spend a small daily budget on it starves the images that could still match.
var (
	PTRLadder   = []time.Duration{week, 2 * week, 4 * week}
	BooruLadder = []time.Duration{week, 2 * week, 4 * week, 8 * week, 16 * week}
)

const week = 7 * 24 * time.Hour

// BooruMaxAttempts is the miss count at which the online ladder gives up and
// the image reads "nothing found".
const BooruMaxAttempts = 6

// PTRCursor is the last applied-update cursor monloader reported, published
// by the footer light's probe and read by the where-builder so `lookup:due`
// sees a fresh value even on a box nobody has a page open on. Zero means no
// probe has landed yet, which the PTR half of the predicate treats as "gate
// on the delay alone" - over-counting rather than under-counting.
var PTRCursor atomic.Uint64

// NextDue returns when a backend may try again after its attempts-th
// consecutive miss, and whether there is a next try at all. A ladder past
// its last rung repeats that rung; the online ladder instead runs out.
func NextDue(now time.Time, backend string, attempts int) (time.Time, bool) {
	ladder := PTRLadder
	if backend == BackendBooru {
		if attempts >= BooruMaxAttempts {
			return time.Time{}, false
		}
		ladder = BooruLadder
	}
	rung := min(max(attempts, 1), len(ladder)) - 1
	return now.Add(ladder[rung]), true
}

// stamp formats a time the way every other timestamp column in the schema is
// stored, so string comparison in SQL orders correctly.
func stamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}
