package archive

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitHub imitates GitHub's release and artifact APIs.
type fakeGitHub struct {
	mu        sync.Mutex
	releases  map[string]map[string]any // by tag: the create request
	ids       map[string]int64
	assets    map[int64]map[string][]byte
	artifacts []Artifact
	zips      map[int64][]byte
	assetIDs  map[string]int64 // release id/name → asset id
	byID      map[int64][]byte
	forbidden bool
	failures  int // server errors before requests succeed
	uploads   int
}

func newFakeGitHub(t *testing.T) (*fakeGitHub, *httptest.Server) {
	f := &fakeGitHub{releases: map[string]map[string]any{}, ids: map[string]int64{}, assets: map[int64]map[string][]byte{}, zips: map[int64][]byte{}, assetIDs: map[string]int64{}, byID: map[int64][]byte{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forbidden {
		http.Error(w, `{"message":"Resource not accessible by integration"}`, http.StatusForbidden)
		return
	}
	if f.failures > 0 {
		f.failures--
		http.Error(w, "unavailable", http.StatusBadGateway)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/repos/org/data")
	switch {
	case strings.HasPrefix(path, "/releases/tags/"):
		tag := strings.TrimPrefix(path, "/releases/tags/")
		id, ok := f.ids[tag]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(release{ID: id, TagName: tag})
	case path == "/releases" && r.Method == http.MethodPost:
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		tag := in["tag_name"].(string)
		f.releases[tag] = in
		f.ids[tag] = int64(len(f.ids) + 1)
		f.assets[f.ids[tag]] = map[string][]byte{}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(release{ID: f.ids[tag], TagName: tag})
	case strings.HasSuffix(path, "/assets") && strings.HasPrefix(path, "/releases/"):
		id, _ := strconv.ParseInt(strings.Split(path, "/")[2], 10, 64)
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			name := r.URL.Query().Get("name")
			if _, ok := f.assets[id][name]; ok {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			f.assets[id][name] = body
			aid := int64(len(f.byID) + 1)
			f.assetIDs[fmt.Sprint(id)+"/"+name] = aid
			f.byID[aid] = body
			f.uploads++
			w.WriteHeader(http.StatusCreated)
			return
		}
		var list []asset
		for name := range f.assets[id] {
			list = append(list, asset{ID: f.assetIDs[fmt.Sprint(id)+"/"+name], Name: name})
		}
		if r.URL.Query().Get("page") != "1" {
			list = nil
		}
		_ = json.NewEncoder(w).Encode(list)
	case strings.HasPrefix(path, "/releases/assets/"):
		aid, _ := strconv.ParseInt(strings.TrimPrefix(path, "/releases/assets/"), 10, 64)
		data, ok := f.byID[aid]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	case path == "/actions/artifacts":
		_ = json.NewEncoder(w).Encode(map[string]any{"artifacts": f.artifacts})
	case strings.HasPrefix(path, "/actions/artifacts/"):
		id, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(path, "/actions/artifacts/"), "/zip"), 10, 64)
		_, _ = w.Write(f.zips[id])
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusTeapot)
	}
}

func testRun(t *testing.T, dir, runID, body string) string {
	t.Helper()
	at, _ := time.Parse("20060102T150405Z", runID[:16])
	w, err := NewWriter(dir, runID, "test", at)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write(Exchange{Source: "pypi", Project: "proj", Target: "pkg", Method: "GET", URL: "https://pypistats.org/api", Status: 200, Body: body}); err != nil {
		t.Fatal(err)
	}
	w.AddJob(Job{Source: "pypi", Project: "proj", Target: "pkg", End: map[string]string{"": "2026-10-20"}})
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return w.Dir()
}

func storeFor(srv *httptest.Server) *ReleaseStore {
	return &ReleaseStore{Client: srv.Client(), Repo: "org/data", APIURL: srv.URL, UploadURL: srv.URL, Wait: time.Millisecond}
}

func TestReleaseUploadAndDownload(t *testing.T) {
	fake, srv := newFakeGitHub(t)
	runDir := testRun(t, t.TempDir(), "20261021T093105Z-123-1", `{"data":[]}`)
	s := storeFor(srv)
	fake.failures = 1 // retried
	if err := s.Upload(context.Background(), runDir); err != nil {
		t.Fatal(err)
	}
	rel := fake.releases["raw-2026-10"]
	if rel == nil || rel["prerelease"] != true || rel["make_latest"] != "false" {
		t.Errorf("release = %+v, want a pre-release that isn't the latest", rel)
	}
	if err := s.Upload(context.Background(), runDir); err != nil || fake.uploads != 2 {
		t.Errorf("a repeated upload uploaded again: %d uploads, %v", fake.uploads, err)
	}
	if ok, err := s.Uploaded(context.Background(), "20261021T093105Z-123-1"); !ok || err != nil {
		t.Errorf("uploaded = %v, %v", ok, err)
	}

	got, err := s.Download(context.Background(), "20261021T093105Z-123-1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := ReadRun(got)
	if err != nil || len(run.Exchanges) != 1 || run.Exchanges[0].Body != `{"data":[]}` {
		t.Errorf("downloaded run = %+v, %v", run, err)
	}
}

func TestReleaseUploadForbidden(t *testing.T) {
	fake, srv := newFakeGitHub(t)
	fake.forbidden = true
	err := storeFor(srv).Upload(context.Background(), testRun(t, t.TempDir(), "20261021T093105Z-123-1", "{}"))
	if !IsPermission(err) || !strings.Contains(err.Error(), "contents: write") {
		t.Errorf("err = %v, want a permission error naming contents: write", err)
	}
}

func zipRun(t *testing.T, runDir string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	entries, _ := os.ReadDir(runDir)
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(runDir, e.Name()))
		fw, _ := zw.Create(e.Name())
		_, _ = fw.Write(data)
	}
	_ = zw.Close()
	return buf.Bytes()
}

// A run after failed uploads finds the pending artifacts in a fresh
// workspace and uploads them; one that was uploaded already is skipped, and
// one pending for six days that still fails is overdue.
func TestCatchUpPending(t *testing.T) {
	fake, srv := newFakeGitHub(t)
	now := time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)
	src := t.TempDir()
	runs := []string{"20261019T093105Z-1-1", "20261020T093105Z-2-1", "20261015T093105Z-3-1"}
	for i, id := range runs {
		fake.artifacts = append(fake.artifacts, Artifact{ID: int64(i + 1), Name: ArtifactName(id), CreatedAt: now.AddDate(0, 0, -(i+1)*2)})
		fake.zips[int64(i+1)] = zipRun(t, testRun(t, src, id, fmt.Sprintf(`{"run":%d}`, i)))
	}
	fake.artifacts = append(fake.artifacts, Artifact{ID: 9, Name: "something-else"})
	s := storeFor(srv)
	if err := s.Upload(context.Background(), filepath.Join(src, runs[1])); err != nil {
		t.Fatal(err)
	}
	fake.zips[3] = []byte("not a zip")

	arts := &Artifacts{Client: srv.Client(), Repo: "org/data", APIURL: srv.URL}
	got, err := CatchUpPending(context.Background(), arts, s, t.TempDir(), []string{"secret"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Uploaded) != 1 || got.Uploaded[0] != runs[0] {
		t.Errorf("uploaded = %v", got.Uploaded)
	}
	if len(got.Skipped) != 1 || got.Skipped[0] != runs[1] {
		t.Errorf("skipped = %v", got.Skipped)
	}
	if len(got.Failed) != 1 || got.Failed[0].RunID != runs[2] || !got.Failed[0].Overdue() {
		t.Errorf("failed = %+v, want the six-day-old run overdue", got.Failed)
	}
}

func TestCatchUpSkipsArchivesWithSecrets(t *testing.T) {
	fake, srv := newFakeGitHub(t)
	now := time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)
	id := "20261020T093105Z-2-1"
	fake.artifacts = []Artifact{{ID: 1, Name: ArtifactName(id), CreatedAt: now.AddDate(0, 0, -1)}}
	fake.zips[1] = zipRun(t, testRun(t, t.TempDir(), id, `{"token":"ghp_secret"}`))
	got, err := CatchUpPending(context.Background(), &Artifacts{Client: srv.Client(), Repo: "org/data", APIURL: srv.URL}, storeFor(srv), t.TempDir(), []string{"ghp_secret"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Failed) != 1 || !strings.Contains(got.Failed[0].Err.Error(), "token") || fake.uploads != 0 {
		t.Errorf("catch-up = %+v with %d uploads, want the archive refused", got, fake.uploads)
	}
}

// A repair range selects, from the releases, every run whose end dates fall
// in it, downloading only the manifests of the others.
func TestReleaseRunsInRange(t *testing.T) {
	_, srv := newFakeGitHub(t)
	s := storeFor(srv)
	src := t.TempDir()
	for _, id := range []string{"20261011T093105Z-1-1", "20261021T093105Z-2-1", "20261101T093105Z-3-1"} {
		at, _ := time.Parse("20060102T150405Z", id[:16])
		w, err := NewWriter(src, id, "test", at)
		if err != nil {
			t.Fatal(err)
		}
		_ = w.Write(Exchange{Source: "pypi", Project: "proj", Target: "pkg", Method: "GET", URL: "https://pypistats.org/api", Status: 200, Body: id})
		w.AddJob(Job{Source: "pypi", Project: "proj", Target: "pkg", End: map[string]string{"": at.AddDate(0, 0, -1).Format("2006-01-02")}})
		if _, err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if err := s.Upload(context.Background(), w.Dir()); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.RunsInRange(context.Background(), "pypi", "proj", "2026-10-15", "2026-10-31", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || filepath.Base(got[0]) != "20261021T093105Z-2-1" || filepath.Base(got[1]) != "20261101T093105Z-3-1" {
		t.Errorf("runs = %v", got)
	}
}
