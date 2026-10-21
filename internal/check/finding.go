// Package check finds problems in stored data and in what a fetch did, and
// reports them as findings.
package check

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Severity says whether a finding can hold data back (Error) or is only
// reported (Warning, Info).
type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
	Info    Severity = "info"
)

// Finding is one problem a check found.
type Finding struct {
	// ID identifies the problem across runs. It's derived from the check,
	// source, project, target and key, never from the message, so the same
	// problem keeps its ID while the numbers in its message change.
	ID       string   `json:"id"`
	Check    string   `json:"check"`
	Severity Severity `json:"severity"`
	Source   string   `json:"source,omitempty"`
	Project  string   `json:"project,omitempty"`
	Target   string   `json:"target,omitempty"`
	// Key says what the finding is about within its target: a metric, the
	// day a problem started, a path. It must not contain counts or other
	// values that change from run to run.
	Key     string   `json:"key,omitempty"`
	Message string   `json:"message"`
	Paths   []string `json:"paths,omitempty"`
	// Accepted is set when velocirepo/checks.toml accepts the finding.
	Accepted bool   `json:"accepted,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// NewFinding returns a finding with its ID set.
func NewFinding(check string, severity Severity, source, project, target, key, message string) Finding {
	f := Finding{
		Check:    check,
		Severity: severity,
		Source:   source,
		Project:  project,
		Target:   target,
		Key:      key,
		Message:  message,
	}
	f.ID = findingID(f)
	return f
}

// WithPaths returns the finding with the files it concerns.
func (f Finding) WithPaths(paths ...string) Finding {
	f.Paths = append(f.Paths, paths...)
	return f
}

func findingID(f Finding) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{f.Check, f.Source, f.Project, f.Target, f.Key}, "\x00")))
	return hex.EncodeToString(sum[:])[:12]
}

// Blocking reports whether the finding is an unaccepted error.
func (f Finding) Blocking() bool {
	return f.Severity == Error && !f.Accepted
}

// Report is what velocirepo check writes to check.json.
type Report struct {
	Findings []Finding `json:"findings"`
}

// HasErrors reports whether the report has an unaccepted error.
func (r Report) HasErrors() bool {
	for _, f := range r.Findings {
		if f.Blocking() {
			return true
		}
	}
	return false
}
