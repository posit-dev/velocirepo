package gate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/posit-dev/velocirepo/internal/check"
)

// StateFile is the gate's state, next to the data directory.
const StateFile = "gate-state.json"

// State is what the gate remembers between runs: the findings that are open.
type State struct {
	Findings []OpenFinding `json:"findings"`
}

// OpenFinding is a finding the gate reported and that isn't resolved yet.
type OpenFinding struct {
	check.Finding
	// Since is the day the finding was first reported.
	Since string `json:"since"`
	// Commit is the commit made by hand the finding is about, if any.
	Commit string `json:"commit,omitempty"`
}

func readState(path string) (State, error) {
	var s State
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("read %s: %w", path, err)
	}
	return s, nil
}

func writeState(path string, s State) error {
	sort.Slice(s.Findings, func(i, j int) bool { return s.Findings[i].ID < s.Findings[j].ID })
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// Changes are how the open findings changed in a run.
type Changes struct {
	New      []OpenFinding
	Resolved []OpenFinding
	Open     []OpenFinding
	// HeldBack are the sources and projects held back after the run.
	HeldBack []string
	// Notes are what manual runs did, such as a release.
	Notes []string
}

// Changed reports whether findings were opened or resolved, or a manual run
// did something.
func (c Changes) Changed() bool { return len(c.New)+len(c.Resolved)+len(c.Notes) > 0 }

// update replaces the open findings with the run's, keeping when each was
// first reported. Accepted findings and notes aren't kept open.
func (s *State) update(findings []OpenFinding, today string) Changes {
	prev := map[string]OpenFinding{}
	for _, f := range s.Findings {
		prev[f.ID] = f
	}
	var c Changes
	seen := map[string]bool{}
	for _, f := range findings {
		if f.Accepted || f.Severity == check.Info || seen[f.ID] {
			continue
		}
		seen[f.ID] = true
		if p, ok := prev[f.ID]; ok {
			f.Since = p.Since
		} else {
			f.Since = today
			c.New = append(c.New, f)
		}
		c.Open = append(c.Open, f)
	}
	for _, f := range s.Findings {
		if !seen[f.ID] {
			c.Resolved = append(c.Resolved, f)
		}
	}
	s.Findings = c.Open
	return c
}
