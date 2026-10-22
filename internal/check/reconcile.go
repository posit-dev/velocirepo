package check

import (
	"context"
	"fmt"
	"strings"

	"github.com/posit-dev/velocirepo/internal/fetch"
	"github.com/posit-dev/velocirepo/internal/reconcile"
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
	var quotas map[string]fetch.Quota
	if c.Results != nil {
		quotas = c.Results.Quota
	}
	results, skipped, err := reconcile.Run(ctx, c.Config, c.DataDir, reconcile.Options{Now: c.Now, Budget: reconcile.NewBudget(quotas), Wait: c.ReconcileWait}, c.Reconcilers)
	if err != nil {
		return nil, err
	}

	var out []Finding
	for _, s := range skipped {
		what := "reconciliation"
		if s.Monthly {
			what = "the per-month reconciliation"
		}
		names := s.Targets
		if len(names) > 10 {
			names = append(append([]string(nil), names[:10]...), fmt.Sprintf("%d more", len(s.Targets)-10))
		}
		out = append(out, NewFinding("quota.low", Warning, s.Source, "", "", s.Quota,
			fmt.Sprintf("%s of %s was skipped to leave %s for everything else; they're checked on a later night", what, strings.Join(names, ", "), s.Quota)))
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
