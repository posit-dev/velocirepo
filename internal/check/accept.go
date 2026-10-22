package check

import (
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

// Acceptance accepts a finding that's known to be a false positive, with a
// reason. It's kept in velocirepo/checks.toml, so accepting is a reviewed
// change.
type Acceptance struct {
	ID      string `toml:"id"`
	Reason  string `toml:"reason"`
	Expires string `toml:"expires"`
}

// Acceptances are the accepted findings.
type Acceptances []Acceptance

type acceptFile struct {
	Accept []Acceptance `toml:"accept"`
}

// LoadAcceptances reads checks.toml; a missing file accepts nothing.
func LoadAcceptances(path string) (Acceptances, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f acceptFile
	if err := toml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, a := range f.Accept {
		if a.ID == "" || a.Reason == "" {
			return nil, fmt.Errorf("%s: every accepted finding needs an id and a reason", path)
		}
		if a.Expires != "" {
			if _, err := time.Parse("2006-01-02", a.Expires); err != nil {
				return nil, fmt.Errorf("%s: expires %q isn't a date", path, a.Expires)
			}
		}
	}
	return f.Accept, nil
}

// Match returns the acceptance of a finding that hasn't expired, if any.
func (as Acceptances) Match(f Finding, now time.Time) (Acceptance, bool) {
	for _, a := range as {
		if a.ID != f.ID {
			continue
		}
		if a.Expires != "" {
			expires, _ := time.Parse("2006-01-02", a.Expires)
			if !now.Before(expires.AddDate(0, 0, 1)) {
				continue
			}
		}
		return a, true
	}
	return Acceptance{}, false
}
