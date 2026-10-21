package check

import (
	"context"
	"fmt"

	"github.com/posit-dev/velocirepo/internal/reconcile"
	"github.com/posit-dev/velocirepo/internal/source"
)

// suspiciousStored is how many stored items make a reference total of zero
// suspicious: an API that suddenly reports nothing is more likely broken than
// the history wrong.
const suspiciousStored = 100

// reconciliation compares stored totals with the APIs' own totals.
type reconciliation struct{}

func (reconciliation) Name() string { return "reconcile" }

func (reconciliation) Run(ctx context.Context, c *Context) ([]Finding, error) {
	if len(c.Reconcilers) == 0 || c.Config == nil {
		return nil, nil
	}
	var out []Finding
	reconcilers := map[string]source.Reconciler{}
	for name, r := range c.Reconcilers {
		if f, low := quotaLow(c, name); low {
			out = append(out, f)
			continue
		}
		reconcilers[name] = r
	}
	results, err := reconcile.Run(ctx, c.Config, c.DataDir, reconcilers)
	if err != nil {
		return nil, err
	}

	unavailable := map[string]bool{}
	for _, r := range results {
		ref := r.Reference
		switch {
		case r.Unavailable != "":
			key := r.Source + "\x00" + r.Project + "\x00" + ref.Target
			if unavailable[key] {
				continue
			}
			unavailable[key] = true
			out = append(out, NewFinding("reconcile.unavailable", Warning, r.Source, r.Project, ref.Target, "",
				"no reference totals to compare with: "+r.Unavailable))
		case ref.Value == 0 && r.Stored >= suspiciousStored:
			out = append(out, NewFinding("reconcile.suspicious-reference", Warning, r.Source, r.Project, ref.Target, ref.Metric,
				fmt.Sprintf("the API reports 0 %s, against %s stored; the reference is more likely wrong than the data", ref.Label, thousands(int(r.Stored)))))
		case r.Differs():
			what := "missing"
			if r.Stored > ref.Value {
				what = "duplicated"
			}
			out = append(out, NewFinding("reconcile.totals", Warning, r.Source, r.Project, ref.Target, ref.Metric,
				fmt.Sprintf("the API reports %s %s, against %s stored (%s data; tolerance %s)", thousands(int(ref.Value)), ref.Label, thousands(int(r.Stored)), what, thousands(int(ref.Tolerance)))))
		}
	}
	return out, nil
}

// quotaLow reports whether a source's quota, as the fetch run left it, is
// below the 10% that reconciliation leaves for everything else.
func quotaLow(c *Context, sourceName string) (Finding, bool) {
	meta, ok := source.MetaFor(sourceName)
	if !ok || meta.ReferenceQuota == "" || c.Results == nil {
		return Finding{}, false
	}
	q, ok := c.Results.Quota[meta.ReferenceQuota]
	if !ok || q.Limit == 0 || q.Remaining*10 >= q.Limit {
		return Finding{}, false
	}
	return NewFinding("quota.low", Warning, sourceName, "", "", "reconcile",
		fmt.Sprintf("reconciliation skipped: %d of %d left of %s", q.Remaining, q.Limit, meta.ReferenceQuota)), true
}
