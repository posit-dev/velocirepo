package fetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/archive"
	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

// routeTo sends every request to srv, so sources keep their real API URLs,
// as the archive does in production.
type routeTo struct{ srv *httptest.Server }

func (r routeTo) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	out.URL.Scheme, out.URL.Host, out.Host = "http", strings.TrimPrefix(r.srv.URL, "http://"), ""
	return http.DefaultTransport.RoundTrip(out)
}

// fakeAPIs answers the archived sources' requests. reactions is the
// reaction count of every issue.
func fakeAPIs(t *testing.T, reactions *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/packages/"):
			_, _ = fmt.Fprint(w, `{"data":[{"category":"without_mirrors","date":"2026-10-19","downloads":5},{"category":"without_mirrors","date":"2026-10-20","downloads":7}]}`)
		case strings.HasPrefix(r.URL.Path, "/api/formula/"):
			_, _ = fmt.Fprint(w, `{"name":"pkg","full_name":"pkg","analytics":{"install":{"30d":{"pkg":10},"90d":{"pkg":20},"365d":{"pkg":30}}}}`)
		case strings.HasSuffix(r.URL.Path, "/traffic/views"):
			_, _ = fmt.Fprint(w, `{"count":3,"uniques":2,"views":[{"timestamp":"2026-10-19T00:00:00Z","count":3,"uniques":2}]}`)
		case strings.HasSuffix(r.URL.Path, "/traffic/clones"):
			_, _ = fmt.Fprint(w, `{"count":1,"uniques":1,"clones":[{"timestamp":"2026-10-19T00:00:00Z","count":1,"uniques":1}]}`)
		case r.URL.Path == "/graphql" && strings.Contains(string(body), "repositoryTopics"):
			_, _ = fmt.Fprint(w, `{"data":{"repository":{"name":"positron","description":"An IDE","url":"https://github.com/posit-dev/positron","createdAt":"2022-01-01T00:00:00Z","pushedAt":"2026-10-20T00:00:00Z","stargazerCount":3400,"repositoryTopics":{"nodes":[]}}}}`)
		case r.URL.Path == "/graphql":
			n := reactions.Load()
			_, _ = fmt.Fprintf(w, `{"data":{"repository":{
				"r0":{"reactions":{"totalCount":%d},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false}}},
				"r1":{"reactions":{"totalCount":%d},"comments":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}}`, n, n+1)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// readAll returns every stored record of a project, sorted.
func readAll(t *testing.T, dataDir string) []string {
	t.Helper()
	var out []string
	for _, src := range []string{"pypi", "homebrew", "github-traffic", "github"} {
		for _, proj := range []string{"proj", "positron"} {
			paths, _ := filepath.Glob(filepath.Join(store.MetricsProjectDir(dataDir, src, proj), "*.jsonl"))
			var records []source.Record
			for _, p := range paths {
				rs, err := store.ReadRecords(p)
				if err != nil {
					t.Fatal(err)
				}
				records = append(records, rs...)
			}
			for _, r := range records {
				out = append(out, fmt.Sprintf("%s %s %s %s %s %d %v", r.Source, r.ProjectID, r.Target, r.Metric, r.Date, r.Value, r.Extra))
			}
		}
	}
	sort.Strings(out)
	return out
}

func archivedFetch(t *testing.T, cfg *config.Config, srv *httptest.Server, now time.Time) archive.Run {
	t.Helper()
	w, err := archive.NewWriter(t.TempDir(), "run1", "test", now)
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Now: func() time.Time { return now }, NoConcatenate: true, WrapTransport: func(http.RoundTripper) http.RoundTripper { return routeTo{srv} }}
	AttachArchive(&opts, w)
	results, err := All(context.Background(), cfg, Tokens{GitHub: "t"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Error != "" {
			t.Fatalf("fetch: %+v", r)
		}
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	run, err := archive.ReadRun(w.Dir())
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// A fetch followed by a replay of its archive writes identical records,
// also after newer data has been stored: the replay makes the original
// requests, from the manifest's options.
func TestReplayRoundTrip(t *testing.T) {
	srv := fakeAPIs(t, &atomic.Int64{})
	dataDir := t.TempDir()
	cfg := &config.Config{Data: config.DataConfig{Dir: dataDir}, Projects: map[string]config.Project{
		"proj": {PyPI: config.StringList{"pkg"}, Homebrew: config.StringList{"pkg"}, GitHubTraffic: config.StringList{"posit-dev/positron"}},
	}}
	run := archivedFetch(t, cfg, srv, time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC))
	want := readAll(t, dataDir)
	if len(want) == 0 {
		t.Fatal("the fetch stored nothing")
	}

	srv.Close() // a replay never touches the network
	for _, d := range []string{"metrics"} {
		if err := os.RemoveAll(dataDir + "/" + d); err != nil {
			t.Fatal(err)
		}
	}
	// Newer data moved the resume points on.
	if err := store.MarkFetchedSeries(dataDir, "pypi", "proj", "", "2026-10-30", []string{"pkg"}); err != nil {
		t.Fatal(err)
	}

	results, err := Replay(context.Background(), cfg, run, ReplayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	replayed := map[string]int{}
	for _, r := range results {
		if r.Error != "" {
			t.Errorf("replay: %+v", r)
		}
		replayed[r.Source] += r.Records
	}
	for _, src := range []string{"pypi", "homebrew", "github-traffic"} {
		if replayed[src] == 0 {
			t.Errorf("%s replayed no records: %+v", src, results)
		}
	}
	if got := readAll(t, dataDir); !reflect.DeepEqual(got, want) {
		t.Errorf("replayed records differ:\n got %v\nwant %v", got, want)
	}
}

func TestReplayUnmatchedRequestFails(t *testing.T) {
	client := &http.Client{Transport: archive.NewReplay(nil)}
	_, err := (&source.Homebrew{Client: client, Formula: "pkg"}).Fetch(context.Background(), source.FetchOptions{EndDate: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)})
	if err == nil || !strings.Contains(err.Error(), "isn't in the archive") {
		t.Errorf("unmatched request = %v, want an error", err)
	}
}

// GitHub is replayed through its snapshot unit only, without any of its
// event requests. With a processing bug fixed, the replay writes the
// corrected records: here, the October total_reactions bug, where a write
// replaced daily files and dropped the reaction counts.
func TestReplayGitHubSnapshots(t *testing.T) {
	var reactions atomic.Int64
	reactions.Store(4)
	srv := fakeAPIs(t, &reactions)
	cfg := &config.Config{Data: config.DataConfig{Dir: t.TempDir()}}
	now := time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)

	w, err := archive.NewWriter(t.TempDir(), "run1", "test", now)
	if err != nil {
		t.Fatal(err)
	}
	capture := &http.Client{Transport: &archive.Capture{Base: routeTo{srv}, Writer: w}}
	g := &source.GitHubEvents{Client: capture, Token: "t", Repo: "posit-dev/positron"}
	opts := source.FetchOptions{ProjectID: "positron", StartDate: now.AddDate(0, 0, -1), EndDate: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)}
	ctx := source.WithRequestInfo(context.Background(), source.RequestInfo{Source: "github", Project: "positron", Target: g.Repo})
	if err := g.FetchSnapshots(ctx, opts, []int{1, 2}); err != nil {
		t.Fatal(err)
	}
	w.AddJob(archiveJob(JobInfo{Source: "github", Project: "positron", Target: g.Repo, Options: opts}))
	w.SetSnapshotRefs("github", "positron", g.Repo, []int{1, 2})
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	run, err := archive.ReadRun(w.Dir())
	if err != nil {
		t.Fatal(err)
	}

	// The buggy write: the day's file lost the reaction counts.
	if _, err := store.WriteRecords(cfg.DataDir(), "github", "positron", []source.Record{{Metric: "daily_stars", ProjectID: "positron", Target: g.Repo, Date: "2026-10-20", Value: 2}}); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	results, err := Replay(context.Background(), cfg, run, ReplayOptions{Source: "github"})
	if err != nil || len(results) != 1 || results[0].Error != "" {
		t.Fatalf("replay = %+v, %v", results, err)
	}
	got := readAll(t, cfg.DataDir())
	for _, want := range []string{
		"github positron posit-dev/positron total_reactions 2026-10-20 4 map[ref:1]",
		"github positron posit-dev/positron total_reactions 2026-10-20 5 map[ref:2]",
		"github positron posit-dev/positron daily_stars 2026-10-20 2 map[]",
	} {
		found := false
		for _, g := range got {
			found = found || g == want
		}
		if !found {
			t.Errorf("missing %q in %v", want, got)
		}
	}
}
