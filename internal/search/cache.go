package search

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Adjacency cache: keyed snapshots of the gallery's sorted match-id
// list, populated by Execute when the page-1 result holds the full
// match set and consumed by ExecuteAdjacent so prev/next is an O(log n)
// slice scan instead of a fresh cursor query.
//
// Sized for the home-LAN deployment: a small handful of concurrent
// queries, each capped at adjacencyCacheMaxIDs so the cache memory
// budget stays bounded even on popular-tag queries. Stale entries fall
// off via TTL; no invalidation hook on writes - inserts/deletes that
// race a browse session may surface a missing prev/next, which the
// detail handler already tolerates.
//
// Cap math: maxEntries * maxIDs * 8 bytes / entry = worst-case bytes.
// 4 * 1 000 000 * 8 = ~32 MB. Average case is far lower because the
// cache only seeds entries whose total fits the cap; sparse queries
// land in single-digit KB. The home-box deployment is single-user
// with a small handful of active tabs, so 4 hot entries cover the
// realistic working set, and the 1 M cap is wide enough to seat
// popular single-tag random-sort queries that would otherwise run
// five parallel temp-sorts under c=5 contention.
const (
	adjacencyCacheTTL        = 5 * time.Minute
	adjacencyCacheMaxEntries = 4
	adjacencyCacheMaxIDs     = 1000000
	// adjacencyFanBudget floors how long Execute's page-1 fan may hold
	// the request; adjacencyFanCostRatio raises that floor for a query
	// that was already expensive to answer.
	//
	// The page query stops at its LIMIT. The fan has no early stop and
	// walks the sort index until it has found every match, so its cost
	// tracks the rows it walks, not the rows it returns - minutes, when
	// the predicate probes hundreds of canonicals per row. Where the two
	// costs are the same order the fan pays for itself: every page-flip
	// and prev/next after it is a slice scan instead of a repeat of a
	// query that was slow once, and the ratio is what keeps those fans
	// alive. The floor covers the queries the page answers cheaply,
	// where the fan is speculative and the plain path is a fine thing to
	// fall back on. Overrunning cancels the read, leaves the entry
	// unset, and the request serves the plain page - the state a query
	// above adjacencyCacheMaxIDs is already in.
	adjacencyFanBudget    = 750 * time.Millisecond
	adjacencyFanCostRatio = 2
)

// fanBudget is how long the fan may run for a request that has already
// spent spent getting to it.
func fanBudget(spent time.Duration) time.Duration {
	return max(adjacencyFanBudget, spent*adjacencyFanCostRatio)
}

type adjacencyCacheEntry struct {
	ids       []int64
	expiresAt time.Time
}

var (
	adjCacheMu      sync.Mutex
	adjCacheEntries = make(map[string]adjacencyCacheEntry)
	adjCacheOrder   []string

	// fanInFlight dedupes background match-id fans launched by Execute
	// when the cache misses: a burst of concurrent cache-miss requests
	// for the same key would otherwise spawn a fan goroutine each, all
	// running the same SELECT and contending for the read pool. With
	// the gate, only the first goroutine fans; the rest see cache miss
	// and skip the populate path, falling back to the regular Execute
	// shape that's already cheap on a single page.
	fanInFlightMu sync.Mutex
	fanInFlight   = map[string]bool{}

	// fanOverBudget holds the keys whose last fan came back empty, until
	// the moment they may be tried again. Without it a query whose fan
	// can't finish inside adjacencyFanBudget pays the whole budget on
	// every page-1 hit and never has anything to show for it; with it
	// the attempt is made once per cache lifetime and the pages in
	// between serve straight off the plain query.
	fanOverBudget = map[string]time.Time{}
)

// AdjacencyCacheTryAcquireFan returns true when the caller wins the
// race to fan the match-ids for key. The winner must call
// AdjacencyCacheReleaseFan on completion regardless of outcome.
// Losers must not fan; the winning goroutine will populate the cache.
// A key whose recent fan came back empty is refused until its hold-off
// lapses.
func AdjacencyCacheTryAcquireFan(key string) bool {
	if key == "" {
		return false
	}
	fanInFlightMu.Lock()
	defer fanInFlightMu.Unlock()
	if fanInFlight[key] {
		return false
	}
	if retryAt, held := fanOverBudget[key]; held {
		if time.Now().Before(retryAt) {
			return false
		}
		delete(fanOverBudget, key)
	}
	fanInFlight[key] = true
	return true
}

// AdjacencyCacheMarkFanOverBudget records that key's fan produced
// nothing to cache, so the next page-1 hit inside the cache lifetime
// serves the plain query instead of spending the budget again.
func AdjacencyCacheMarkFanOverBudget(key string) {
	if key == "" {
		return
	}
	fanInFlightMu.Lock()
	fanOverBudget[key] = time.Now().Add(adjacencyCacheTTL)
	fanInFlightMu.Unlock()
}

// AdjacencyCacheReleaseFan releases the in-flight gate so the next
// cache miss after TTL expiry can fan again.
func AdjacencyCacheReleaseFan(key string) {
	fanInFlightMu.Lock()
	delete(fanInFlight, key)
	fanInFlightMu.Unlock()
}

// AdjacencyCacheGet returns the cached sorted match-id list for key,
// or ok=false on miss / expiry. The returned slice aliases the cached
// backing array (copying it on every hit would blow the adjacency-cache
// latency budget on large result sets), so callers must treat it as
// read-only - never sort, append into, or mutate it.
func AdjacencyCacheGet(key string) ([]int64, bool) {
	if key == "" {
		return nil, false
	}
	adjCacheMu.Lock()
	defer adjCacheMu.Unlock()
	entry, ok := adjCacheEntries[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(adjCacheEntries, key)
		removeFromOrder(key)
		return nil, false
	}
	return entry.ids, true
}

// AdjacencyCacheSet stores ids for key under the configured TTL. Empty
// keys, empty lists, and lists above adjacencyCacheMaxIDs are skipped
// so a popular query can't push the cache over its memory budget.
//
// Re-setting an existing key (typical after the entry's TTL expired
// without an intervening Get to remove it) refreshes its slot in the
// LRU order so a freshly written entry isn't immediately evicted by
// the next unrelated Set.
func AdjacencyCacheSet(key string, ids []int64) {
	if key == "" || len(ids) == 0 || len(ids) > adjacencyCacheMaxIDs {
		return
	}
	adjCacheMu.Lock()
	defer adjCacheMu.Unlock()
	if _, exists := adjCacheEntries[key]; exists {
		removeFromOrder(key)
	}
	adjCacheOrder = append(adjCacheOrder, key)
	snapshot := make([]int64, len(ids))
	copy(snapshot, ids)
	adjCacheEntries[key] = adjacencyCacheEntry{
		ids:       snapshot,
		expiresAt: time.Now().Add(adjacencyCacheTTL),
	}
	for len(adjCacheOrder) > adjacencyCacheMaxEntries {
		oldest := adjCacheOrder[0]
		adjCacheOrder = adjCacheOrder[1:]
		delete(adjCacheEntries, oldest)
	}
}

// AdjacencyCacheDropForGallery drops every entry whose key starts with
// the given gallery name. Called from a gallery's InvalidateCaches so a
// cached match-id list can't survive a write that changed result-set
// membership (delete, move, inbox/favourite toggle, batch tag, ...). The
// per-gallery cap is small enough that walking the map on every write
// is cheap; a global Clear would also drop other galleries' entries
// unnecessarily.
func AdjacencyCacheDropForGallery(gallery string) {
	if gallery == "" {
		return
	}
	prefix := gallery + "\x00"
	adjCacheDrop(func(k string, _ adjacencyCacheEntry) bool {
		return strings.HasPrefix(k, prefix)
	})
	dropFanHoldOffs(func(k string, _ time.Time) bool {
		return strings.HasPrefix(k, prefix)
	})
}

// AdjacencyCacheSweep drops every entry past its TTL. Get evicts one
// on the way past and Set evicts by LRU, so without this an idle
// process keeps expired lists - up to the cache's whole budget - until
// something touches the cache again. The fan hold-offs have no such
// eviction of their own: nothing reads a key that stopped being asked
// for, so the sweep is what keeps the map proportional to live traffic.
func AdjacencyCacheSweep() {
	now := time.Now()
	adjCacheDrop(func(_ string, entry adjacencyCacheEntry) bool {
		return now.After(entry.expiresAt)
	})
	dropFanHoldOffs(func(_ string, retryAt time.Time) bool {
		return now.After(retryAt)
	})
}

func dropFanHoldOffs(drop func(string, time.Time) bool) {
	fanInFlightMu.Lock()
	maps.DeleteFunc(fanOverBudget, drop)
	fanInFlightMu.Unlock()
}

// adjCacheDrop removes every entry drop reports and rebuilds the LRU
// order around the survivors. len(adjCacheOrder) stays bounded by
// adjacencyCacheMaxEntries (4), so the rebuild is constant time.
func adjCacheDrop(drop func(string, adjacencyCacheEntry) bool) {
	adjCacheMu.Lock()
	defer adjCacheMu.Unlock()
	maps.DeleteFunc(adjCacheEntries, drop)
	adjCacheOrder = slices.DeleteFunc(adjCacheOrder, func(k string) bool {
		_, kept := adjCacheEntries[k]
		return !kept
	})
}

func removeFromOrder(key string) {
	if i := slices.Index(adjCacheOrder, key); i >= 0 {
		adjCacheOrder = slices.Delete(adjCacheOrder, i, i+1)
	}
}

// BuildAdjacencyCacheKey returns the stable key the gallery's Execute
// and the detail's ExecuteAdjacent use for the same browsing session.
// The components are joined NUL-separated so substrings can't collide
// across boundaries (a query "foo|bar" is still distinct from a
// gallery "foo" + query "bar"). A zero seed under a non-random sort
// is normalised to the empty seed so newest/filesize sorts hit the
// cache regardless of any leftover seed param on the URL.
func BuildAdjacencyCacheKey(gallery, query, sort, order string, seed int64, ceiling string) string {
	seedStr := ""
	if sort == "random" && seed != 0 {
		seedStr = strconv.FormatInt(seed, 10)
	}
	return strings.Join([]string{gallery, query, sort, order, seedStr, ceiling}, "\x00")
}

// findInAdjacencyList returns the prev/next image ids around currentID
// in a sorted match-id list. Returns nil pointers for out-of-bounds
// neighbours; (nil, nil) when currentID isn't in the list (typically
// because it was deleted or the list belongs to a different query).
func findInAdjacencyList(ids []int64, currentID int64) (*int64, *int64) {
	i := slices.Index(ids, currentID)
	if i < 0 {
		return nil, nil
	}
	var prev, next *int64
	if i > 0 {
		p := ids[i-1]
		prev = &p
	}
	if i < len(ids)-1 {
		n := ids[i+1]
		next = &n
	}
	return prev, next
}
