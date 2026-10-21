package source

import (
	"context"
	"sort"
	"sync"
	"time"
	// Time zones are looked up by name, also on systems without tzdata.
	_ "time/tzdata"
)

// ArchiveMode says whether a response is kept in the raw archive.
type ArchiveMode int

const (
	// ArchiveUndeclared is the zero value. A source must declare a mode;
	// undeclared responses are never archived.
	ArchiveUndeclared ArchiveMode = iota
	// NotArchived is for data that can be fetched again, where a re-fetch is
	// the repair.
	NotArchived
	// ArchiveFull keeps the whole response: data the API forgets, from
	// public targets or responses that contain only what was asked for.
	ArchiveFull
	// ArchiveReadFields keeps only the fields velocirepo reads, after the
	// same filter as the stored data (LinkedIn).
	ArchiveReadFields
)

// Meta declares what the rest of velocirepo needs to know about a source.
type Meta struct {
	Name string
	// Freshness is the most days allowed between a target's watermark and
	// today, per series. "" is the source's main series; GitHub's star
	// history is the series "stars".
	Freshness map[string]int
	// Archive is the archive mode of the source's responses. A source can
	// override it for single requests with WithArchiveMode.
	Archive ArchiveMode
	// Reduce turns a response into the fields velocirepo reads, for
	// ArchiveReadFields. It returns ok=false for a response that must not be
	// archived at all, such as a draft post.
	Reduce func(url string, body []byte) (reduced []byte, ok bool)
	// Reconciles is set for sources that implement Reconciler.
	Reconciles bool
	// NoReferenceTotals says why a source doesn't reconcile.
	NoReferenceTotals string
	// ReferenceQuota is the quota the reference requests use, as fetch
	// --results names it, such as "api.github.com/graphql".
	ReferenceQuota string
	// RemovableEvents are the event types that can disappear upstream, such
	// as deleted comments, so a re-fetch that no longer has them isn't an
	// error.
	RemovableEvents []string
}

var (
	metaMu sync.RWMutex
	metas  = map[string]Meta{}
)

// Register declares a source's metadata. Every source calls it from its own
// file.
func Register(m Meta) {
	metaMu.Lock()
	defer metaMu.Unlock()
	metas[m.Name] = m
}

// MetaFor returns a source's metadata.
func MetaFor(name string) (Meta, bool) {
	metaMu.RLock()
	defer metaMu.RUnlock()
	m, ok := metas[name]
	return m, ok
}

// Metas returns every registered source's metadata, sorted by name.
func Metas() []Meta {
	metaMu.RLock()
	defer metaMu.RUnlock()
	out := make([]Meta, 0, len(metas))
	for _, m := range metas {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// FreshnessLimit returns the freshness limit of a source's series, falling
// back to its main series.
func FreshnessLimit(sourceName, series string) (int, bool) {
	m, ok := MetaFor(sourceName)
	if !ok {
		return 0, false
	}
	if limit, ok := m.Freshness[series]; ok {
		return limit, true
	}
	limit, ok := m.Freshness[""]
	return limit, ok
}

type archiveModeKey struct{}

// WithArchiveMode marks the requests made with ctx with an archive mode,
// overriding the source's default.
func WithArchiveMode(ctx context.Context, mode ArchiveMode) context.Context {
	return context.WithValue(ctx, archiveModeKey{}, mode)
}

// ArchiveModeOf returns the archive mode set with WithArchiveMode, if any.
func ArchiveModeOf(ctx context.Context) (ArchiveMode, bool) {
	mode, ok := ctx.Value(archiveModeKey{}).(ArchiveMode)
	return mode, ok
}

// DayZoner is implemented by sources whose days don't end at UTC midnight.
// series is "" for the source's main series.
type DayZoner interface {
	DayZone(ctx context.Context, series string) (*time.Location, error)
}

// Publisher is implemented by sources that publish their data late. It
// returns the latest day the source has published, for all its targets.
type Publisher interface {
	PublishedThrough(ctx context.Context) (time.Time, error)
}

// ReferenceTotal is a total that an API computes itself, for reconciliation.
type ReferenceTotal struct {
	Target string
	// Kind says what's compared with Value: "events", the number of stored
	// events of type Metric, or "records", the sum of the stored records of
	// metric Metric.
	Kind   string
	Metric string
	// Label is what's counted, for messages, such as "issues".
	Label string
	Value int64
	// Tolerance is how far the stored total may differ from Value, for
	// example by what happened since the last day fetched.
	Tolerance int64
	// From and Through bound the period the total covers; a zero From means
	// all time.
	From    time.Time
	Through time.Time
	// Unavailable says why the total couldn't be read, such as a repository
	// that doesn't exist anymore; Value is meaningless then.
	Unavailable string
}

// Reconciler is implemented by sources that can report reference totals.
type Reconciler interface {
	ReferenceTotals(ctx context.Context, targets []string) ([]ReferenceTotal, error)
}

// RequestInfo says which source, project and target a request is made for.
// The fetch orchestrator sets it on the context it fetches a target with, so
// the archive knows where each response belongs.
type RequestInfo struct {
	Source  string
	Project string
	Target  string
}

type requestInfoKey struct{}

// WithRequestInfo marks the requests made with ctx as being for a source,
// project and target.
func WithRequestInfo(ctx context.Context, info RequestInfo) context.Context {
	return context.WithValue(ctx, requestInfoKey{}, info)
}

// RequestInfoOf returns the information set with WithRequestInfo, if any.
func RequestInfoOf(ctx context.Context) (RequestInfo, bool) {
	info, ok := ctx.Value(requestInfoKey{}).(RequestInfo)
	return info, ok
}
