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
}

// WriteResults writes a run's results as JSON.
func WriteResults(path string, results []Result, runTime time.Time) error {
	data, err := json.MarshalIndent(RunResults{RunTime: runTime.UTC(), Results: results}, "", "  ")
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
