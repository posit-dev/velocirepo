package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// CRAN publishes its downloads two days late. cranlogs' last-day endpoint
// says how far, so watermarks don't move past a day that isn't published.
func TestCRANPublishedThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/downloads/total/last-day/dplyr" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"start":"2026-10-19","end":"2026-10-19","downloads":74717,"package":"dplyr"}]`))
	}))
	defer srv.Close()

	got, err := (&CRAN{Client: srv.Client(), Package: "dplyr", BaseURL: srv.URL}).PublishedThrough(context.Background())
	if err != nil || got.Format("2006-01-02") != "2026-10-19" {
		t.Errorf("PublishedThrough = %v, %v; want 2026-10-19", got, err)
	}
}

// PyPI leaves out days without downloads, so the latest day of a quiet
// package says nothing about how far pypistats has published. A reference
// package with downloads every day does.
func TestPyPIPublishedThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/packages/pip/overall" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[
			{"category":"without_mirrors","date":"2026-10-19","downloads":1},
			{"category":"without_mirrors","date":"2026-10-20","downloads":2},
			{"category":"with_mirrors","date":"2026-10-21","downloads":3}
		]}`))
	}))
	defer srv.Close()

	got, err := (&PyPI{Client: srv.Client(), Package: "raghilda", BaseURL: srv.URL}).PublishedThrough(context.Background())
	if err != nil || got.Format("2006-01-02") != "2026-10-20" {
		t.Errorf("PublishedThrough = %v, %v; want 2026-10-20", got, err)
	}
}
