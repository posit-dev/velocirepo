package check

import (
	"context"
	"fmt"

	"github.com/posit-dev/velocirepo/internal/fetch"
)

// freshness reports targets whose watermark lags behind today (UTC) by more
// than their source's freshness limit: a target that keeps failing, or a
// source that stopped publishing. A target without a watermark yet isn't
// reported; its first fetch gives it one, or fails and is reported as such.
type freshness struct{}

func (freshness) Name() string { return "data.freshness" }

func (freshness) Run(_ context.Context, c *Context) ([]Finding, error) {
	if c.Config == nil {
		return nil, nil
	}
	all, err := fetch.Freshness(c.Config, c.DataDir, c.Now)
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, f := range all {
		if !f.Stale() {
			continue
		}
		what := "fetched"
		if f.Series != "" {
			what = "its " + f.Series + " fetched"
		}
		msg := fmt.Sprintf("%s was last %s through %s, %d days behind; the limit is %d", f.Target, what, f.Watermark, f.Lag, f.Limit)
		out = append(out, NewFinding("data.freshness", Warning, f.Source, f.Project, f.Target, f.Series, msg))
	}
	return out, nil
}
