package check

import (
	"encoding/json"
	"testing"
)

func TestFindingIDIsStable(t *testing.T) {
	a := NewFinding("diff.records-removed", Error, "github", "positron", "posit-dev/positron", "events", "57,155 events removed")
	b := NewFinding("diff.records-removed", Error, "github", "positron", "posit-dev/positron", "events", "57,155 events removed")
	if a.ID != b.ID {
		t.Errorf("same input gave IDs %s and %s", a.ID, b.ID)
	}

	other := NewFinding("diff.records-removed", Error, "github", "positron", "posit-dev/ark", "events", "57,155 events removed")
	if other.ID == a.ID {
		t.Errorf("different targets gave the same ID %s", a.ID)
	}

	// The message carries counts that change from night to night; the ID
	// must not.
	later := NewFinding("diff.records-removed", Error, "github", "positron", "posit-dev/positron", "events", "57,160 events removed")
	if later.ID != a.ID {
		t.Errorf("a different message changed the ID: %s and %s", a.ID, later.ID)
	}
}

func TestReportJSON(t *testing.T) {
	r := Report{Findings: []Finding{
		NewFinding("fetch.failed", Error, "cran", "dplyr", "", "", "boom").WithPaths("data/metrics/cran/dplyr/"),
	}}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var back Report
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Findings) != 1 || back.Findings[0].ID != r.Findings[0].ID || back.Findings[0].Paths[0] != "data/metrics/cran/dplyr/" {
		t.Errorf("round trip lost data: %s", data)
	}
	if !r.HasErrors() {
		t.Error("HasErrors() = false for a report with an error")
	}
}
