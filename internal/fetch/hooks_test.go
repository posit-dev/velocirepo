package fetch

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/source"
)

// The archive and replay hook into fetch without editing it: OnJob sees every
// job's resolved options, and Concatenate replaces the concatenation step.
func TestFetchHooks(t *testing.T) {
	cfg := &config.Config{Data: config.DataConfig{Dir: t.TempDir()}, Projects: map[string]config.Project{"proj": {}}}
	var jobs []JobInfo
	concatenated := false
	_, err := Source(context.Background(), cfg, Tokens{}, "pypi",
		Options{
			StartDate:   "2025-03-01",
			EndDate:     "2025-03-02",
			OnJob:       func(j JobInfo) { jobs = append(jobs, j) },
			Concatenate: func(string, time.Time) error { concatenated = true; return nil },
		},
		func(string, config.Project) []source.Source { return []source.Source{rangeSource{target: "pkg"}} },
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Source != "pypi" || jobs[0].Project != "proj" || jobs[0].Options.StartDate.Format("2006-01-02") != "2025-03-01" {
		t.Errorf("OnJob saw %+v", jobs)
	}
	if !concatenated {
		t.Error("Concatenate wasn't called")
	}
}

type recordingTransport struct{ used *bool }

func (r recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	*r.used = true
	return nil, http.ErrHandlerTimeout
}

func TestWrapTransport(t *testing.T) {
	used := false
	opts := Options{WrapTransport: func(http.RoundTripper) http.RoundTripper { return recordingTransport{&used} }}
	client := clientForOpts(&opts)
	req, _ := http.NewRequest(http.MethodGet, "http://example.invalid/", nil)
	_, _ = client.Do(req)
	if !used {
		t.Error("the wrapped transport wasn't used")
	}
}

func TestWriteResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.json")
	results := []Result{{Source: "cran", ProjectID: "dplyr", Records: 3, Error: "boom"}}
	if err := WriteResults(path, results, time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var back RunResults
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Results) != 1 || back.Results[0].Error != "boom" || back.RunTime.Hour() != 9 {
		t.Errorf("results = %+v", back)
	}
}
