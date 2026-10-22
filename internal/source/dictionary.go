package source

// MetricKind says how the values of a metric combine over days.
type MetricKind int

const (
	// MetricUndeclared is the zero value; every metric declares its kind.
	MetricUndeclared MetricKind = iota
	// MetricDaily is an amount per day, such as downloads: days can be summed.
	MetricDaily
	// MetricTotal is a running total or a snapshot, such as a subscriber count.
	// It's stored only on the days it changes, so it's never summed over
	// days; metrics_filled carries it forward to every day.
	MetricTotal
	// MetricWindow is a snapshot of a rolling window, such as the installs of the
	// last 30 days, stored on every day it's fetched. Days can't be summed.
	MetricWindow
)

// String returns the kind as the data dictionary names it.
func (k MetricKind) String() string {
	switch k {
	case MetricDaily:
		return "daily"
	case MetricTotal:
		return "total"
	case MetricWindow:
		return "window"
	}
	return "undeclared"
}

// MetricInfo describes a metric a source writes.
type MetricInfo struct {
	Name        string
	Kind        MetricKind
	Description string
	// Key is the column that identifies a sub-series, such as "page" for a
	// per-page series; "" for one series per target. It names one of the
	// source's Columns.
	Key string
	// Measure names a governed sum of a Daily metric, such as
	// "cran_downloads", with its description.
	Measure            string
	MeasureDescription string
}

// EventInfo describes an event type a source writes.
type EventInfo struct {
	Name        string
	Description string
}

// ContentInfo describes a content type a source writes. Name is "" for
// entries without a type, such as RSS items whose feed sets none.
type ContentInfo struct {
	Name        string
	Description string
}

// ColumnInfo is an extra key that the export turns into a column of its own,
// so queries need no JSON functions.
type ColumnInfo struct {
	// Table is "metrics" (which also covers metrics_filled and indicators)
	// or "content".
	Table string
	Key   string
	// Type is "string" or "integer".
	Type        string
	Description string
	// Identifies names the content column a metrics column matches within
	// a project, source and target: "id" for a video id, "ref" for an issue
	// number; "" if none.
	Identifies string
}
