package ci

import (
	"os"
	"strconv"
	"time"
)

// ciDays is how far back the runs are kept (CI_DAYS, 90 by default).
func ciDays() float64 {
	if v, err := strconv.ParseFloat(os.Getenv("CI_DAYS"), 64); err == nil && v >= 0 {
		return v
	}
	return 90
}

// WindowFloor is the oldest run kept: CI_DAYS back from now. The window is always this, whatever the sync.
func WindowFloor(now time.Time) time.Time {
	return time.UnixMilli(now.UnixMilli() - int64(ciDays()*86_400_000))
}

// ReadFrom is where this sync starts reading the host: the window, or a day before the last sync (a later sync reads
// less). It never reaches before the window.
func ReadFrom(now time.Time, st *Store) time.Time {
	floor := WindowFloor(now)
	st.mu.RLock()
	last := st.meta.LastSync
	st.mu.RUnlock()
	if last == nil {
		return floor
	}
	t, err := time.Parse(time.RFC3339Nano, *last)
	if err != nil {
		return floor
	}
	if incremental := t.Add(-24 * time.Hour); incremental.After(floor) {
		return incremental
	}
	return floor
}
