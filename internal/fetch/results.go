package fetch

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// RunResults is what fetch --results writes: what every job of a run did, so
// that check and gate can read it without parsing logs.
type RunResults struct {
	RunTime time.Time `json:"run_time"`
	Results []Result  `json:"results"`
	// Quota is the rate limit left per API at the end of the run.
	Quota map[string]Quota `json:"quota,omitempty"`
	// Deprecations are endpoints that announced their removal.
	Deprecations []string `json:"deprecations,omitempty"`
}

// WriteResults writes a run's results as JSON, with the quota and
// deprecations stats saw, if any.
func WriteResults(path string, results []Result, runTime time.Time, stats ...*Stats) error {
	r := RunResults{RunTime: runTime.UTC(), Results: results}
	if len(stats) > 0 && stats[0] != nil {
		r.Quota = stats[0].QuotaSnapshot()
		r.Deprecations = stats[0].DeprecationsSnapshot()
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encode results: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write results: %w", err)
	}
	return nil
}

// ReadResults reads a file written by WriteResults.
func ReadResults(path string) (RunResults, error) {
	var r RunResults
	data, err := os.ReadFile(path)
	if err != nil {
		return r, fmt.Errorf("read results: %w", err)
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, fmt.Errorf("parse results %s: %w", path, err)
	}
	return r, nil
}
