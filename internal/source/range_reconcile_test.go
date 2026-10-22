package source

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var september = [2]time.Time{time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)}

func TestCRANMonthlyReferenceTotals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/downloads/total/2026-09-01:2026-09-30/dplyr" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"start":"2026-09-01","end":"2026-09-30","downloads":1823444,"package":"dplyr"}]`))
	}))
	defer srv.Close()
	totals, err := (&CRAN{Client: srv.Client(), BaseURL: srv.URL}).MonthlyReferenceTotals(context.Background(), []string{"dplyr"}, september[0], september[1], nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(totals) != 1 || totals[0].Value != 1823444 || totals[0].Metric != "daily_downloads" || totals[0].Tolerance != 18234 {
		t.Errorf("totals = %+v", totals)
	}
}

func TestPlausibleMonthlyReferenceTotals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var q map[string]any
		_ = json.Unmarshal(body, &q)
		if _, ok := q["dimensions"]; ok || !strings.Contains(string(body), `"date_range":["2026-09-01","2026-09-30"]`) {
			t.Errorf("query = %s", body)
		}
		_, _ = w.Write([]byte(`{"results":[{"metrics":[899,352],"dimensions":[]}]}`))
	}))
	defer srv.Close()
	totals, err := (&Plausible{Client: srv.Client(), BaseURL: srv.URL, APIKey: "k"}).MonthlyReferenceTotals(context.Background(), []string{"example.com"}, september[0], september[1], nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(totals) != 2 || totals[0].Metric != "daily_site_pageviews" || totals[0].Value != 899 || totals[1].Metric != "daily_site_visits" || totals[1].Value != 352 {
		t.Errorf("totals = %+v", totals)
	}
}
