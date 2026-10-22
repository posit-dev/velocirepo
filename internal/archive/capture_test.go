package archive

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/posit-dev/velocirepo/internal/source"
)

func init() {
	source.Register(source.Meta{Name: "test-archived", Freshness: map[string]int{"": 1}, Archive: source.ArchiveFull, NoReferenceTotals: "test"})
	source.Register(source.Meta{Name: "test-refetchable", Freshness: map[string]int{"": 1}, Archive: source.NotArchived, NoReferenceTotals: "test"})
	source.Register(source.Meta{Name: "test-readfields", Freshness: map[string]int{"": 1}, Archive: source.ArchiveReadFields, NoReferenceTotals: "test",
		Reduce: func(url string, body []byte) ([]byte, bool) {
			if strings.Contains(string(body), "draft") {
				return nil, false
			}
			return []byte(`{"kept":true}`), true
		}})
}

func get(t *testing.T, client *http.Client, ctx context.Context, url string) string {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer secret-token-123")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

func TestCaptureArchivesOnlyWhatCantBeFetchedAgain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/draft" {
			_, _ = w.Write([]byte(`{"state":"draft"}`))
			return
		}
		w.Header().Set("X-RateLimit-Remaining", "4999")
		_, _ = w.Write([]byte(`{"value":42}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	w, err := NewWriter(dir, "run1", "v1.1.0", time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &Capture{Base: http.DefaultTransport, Writer: w}}
	ctxFor := func(src string) context.Context {
		return source.WithRequestInfo(context.Background(), source.RequestInfo{Source: src, Project: "proj", Target: "t"})
	}

	if got := get(t, client, ctxFor("test-archived"), srv.URL+"/snap?key=AIzaSECRET&page=1"); got != `{"value":42}` {
		t.Errorf("the source got %q, want the response unchanged", got)
	}
	get(t, client, ctxFor("test-refetchable"), srv.URL+"/events")
	get(t, client, source.WithArchiveMode(ctxFor("test-refetchable"), source.ArchiveFull), srv.URL+"/marked")
	get(t, client, ctxFor("test-readfields"), srv.URL+"/post")
	get(t, client, ctxFor("test-readfields"), srv.URL+"/draft")
	get(t, client, context.Background(), srv.URL+"/no-info")

	w.AddJob(Job{Source: "test-archived", Project: "proj", Target: "t", Start: map[string]string{"": "2026-10-20"}, End: map[string]string{"": "2026-10-20"}})
	manifest, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}

	exchanges := map[string][]Exchange{}
	for _, f := range manifest.Files {
		ex, err := ReadExchanges(filepath.Join(dir, "run1", f.Name))
		if err != nil {
			t.Fatal(err)
		}
		if len(ex) != f.Exchanges || f.SHA256 == "" {
			t.Errorf("file %s: %d exchanges, manifest says %d", f.Name, len(ex), f.Exchanges)
		}
		exchanges[f.Source] = ex
	}

	archived := exchanges["test-archived"]
	if len(archived) != 1 {
		t.Fatalf("archived source: %d exchanges, want 1", len(archived))
	}
	if strings.Contains(archived[0].URL, "AIzaSECRET") || !strings.Contains(archived[0].URL, "key=REDACTED") || !strings.Contains(archived[0].URL, "page=1") {
		t.Errorf("URL not redacted: %s", archived[0].URL)
	}
	if archived[0].Body != `{"value":42}` || archived[0].Status != 200 || archived[0].Headers["X-Ratelimit-Remaining"] != "4999" {
		t.Errorf("exchange = %+v", archived[0])
	}
	if refetch := exchanges["test-refetchable"]; len(refetch) != 1 || !strings.HasSuffix(refetch[0].URL, "/marked") {
		t.Errorf("re-fetchable source: %+v, want only the request marked for the archive", refetch)
	}
	if rf := exchanges["test-readfields"]; len(rf) != 1 || rf[0].Body != `{"kept":true}` {
		t.Errorf("read-fields source: %+v, want the reduced post and no draft", rf)
	}
	if len(manifest.Jobs) != 1 || manifest.Jobs[0].Start[""] != "2026-10-20" {
		t.Errorf("manifest jobs = %+v", manifest.Jobs)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "run1", "manifest.json"))
	if !strings.Contains(string(data), `"run_id": "run1"`) {
		t.Errorf("manifest.json = %s", data)
	}
	found, err := FindSecrets(filepath.Join(dir, "run1"), []string{"secret-token-123", "AIzaSECRET", ""})
	if err != nil || len(found) != 0 {
		t.Errorf("FindSecrets = %v, %v; want none (headers aren't captured, URLs are redacted)", found, err)
	}
}

func TestRedactRequestBody(t *testing.T) {
	got := string(redactBody([]byte(`{"query":"q","variables":{"token":"abc","owner":"o"},"api_key":"k"}`)))
	if strings.Contains(got, "abc") || strings.Contains(got, `"k"`) || !strings.Contains(got, `"owner":"o"`) {
		t.Errorf("redactBody = %s", got)
	}
	if got := string(redactBody([]byte("not json"))); got != "not json" {
		t.Errorf("redactBody of a non-JSON body = %q", got)
	}
}

func TestFindSecrets(t *testing.T) {
	dir := t.TempDir()
	w, _ := NewWriter(dir, "run", "v", time.Now())
	_ = w.Write(Exchange{Source: "test-archived", URL: "http://x/", Body: "leaked supersecretvalue here"})
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}
	found, err := FindSecrets(filepath.Join(dir, "run"), []string{"supersecretvalue"})
	if err != nil || len(found) != 1 {
		t.Errorf("FindSecrets = %v, %v; want the leaked secret", found, err)
	}
}
