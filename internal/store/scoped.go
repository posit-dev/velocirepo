package store

import (
	"time"

	"github.com/posit-dev/velocirepo/internal/dateutil"
	"github.com/posit-dev/velocirepo/internal/source"
)

// windowCovers reports whether a window holds a day (YYYY-MM-DD).
func windowCovers(w source.Window, date string) bool {
	d, err := dateutil.ParseDate(date)
	if err != nil {
		return false
	}
	if !w.Start.IsZero() && d.Before(w.Start) {
		return false
	}
	return !d.After(w.End.Truncate(24 * time.Hour))
}
