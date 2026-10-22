package reconcile

import (
	"context"
	"sort"
	"time"

	"github.com/posit-dev/velocirepo/internal/fetch"
)

// Reconciliation gives way to everything else: it uses at most budgetShare
// of a shared quota per run, and none of what's left below that share.
const budgetShare = 0.10

// SearchQuota is the search API's quota, which only reconciliation uses:
// fetch makes no search requests. It has its own budget per night, at no
// more than searchesPerMinute. GitHub allows 30 searches a minute; the
// margin keeps a minute that resets a little late from refusing the last.
const (
	SearchQuota       = "api.github.com/search"
	SearchBudget      = 270
	searchesPerMinute = 25
)

// Budget tracks how much of each quota reconciliation may still use in a run.
type Budget struct {
	quotas map[string]fetch.Quota
	spent  map[string]int
}

// NewBudget starts a run's budget from the quotas the fetch left.
func NewBudget(quotas map[string]fetch.Quota) *Budget {
	return &Budget{quotas: quotas, spent: map[string]int{}}
}

// Allow reports whether n more requests fit the budget of a quota, and
// counts them if so. A quota that the fetch didn't report is assumed to be
// full, except the search budget, which is always counted.
func (b *Budget) Allow(key string, n int) bool {
	if key == SearchQuota {
		if b.spent[key]+n > SearchBudget {
			return false
		}
		b.spent[key] += n
		return true
	}
	q, ok := b.quotas[key]
	if !ok || q.Limit == 0 {
		b.spent[key] += n
		return true
	}
	share := int(float64(q.Limit) * budgetShare)
	if b.spent[key]+n > share || q.Remaining-b.spent[key]-n < share {
		return false
	}
	b.spent[key] += n
	return true
}

// Spent returns how many requests of a quota were counted.
func (b *Budget) Spent(key string) int { return b.spent[key] }

// Pacer spaces requests so that no more than a rate per minute are made.
type Pacer struct {
	interval time.Duration
	last     time.Time
	// Sleep waits; nil means time.Sleep, honouring the context.
	Sleep func(ctx context.Context, d time.Duration) error
	Now   func() time.Time
}

// NewSearchPacer paces the search API.
func NewSearchPacer() *Pacer {
	return &Pacer{interval: time.Minute / searchesPerMinute}
}

// Wait waits until the next request may be made.
func (p *Pacer) Wait(ctx context.Context) error {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	sleep := p.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
				return nil
			}
		}
	}
	if !p.last.IsZero() {
		if d := p.interval - now().Sub(p.last); d > 0 {
			if err := sleep(ctx, d); err != nil {
				return err
			}
		}
	}
	p.last = now()
	return nil
}

// rotationNights is how many nights the per-month checks are spread over.
const rotationNights = 7

// Tonight returns the targets whose last complete month is checked tonight:
// each target on one night of every seven, by its place in the sorted list,
// so over a week every target is checked once and the nights get equal
// shares.
func Tonight(targets []string, now time.Time) []string {
	night := int(now.UTC().Unix()/86400) % rotationNights
	sorted := append([]string(nil), targets...)
	sort.Strings(sorted)
	var out []string
	for i, t := range sorted {
		if i%rotationNights == night {
			out = append(out, t)
		}
	}
	return out
}

// LastCompleteMonth returns the first and last day of the month before now's,
// in UTC, the time zone the events are stored in.
func LastCompleteMonth(now time.Time) (time.Time, time.Time) {
	y, m, _ := now.UTC().Date()
	first := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
	return first, first.AddDate(0, 1, -1)
}
