package source

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestPlausibleFetch(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		assertBearerToken(t, r, "test-key")

		callCount++
		var resp interface{}
		if callCount == 1 {
			resp = map[string]interface{}{
				"results": []map[string]interface{}{
					{"dimensions": []string{"2025-06-01"}, "metrics": []int64{100, 50, 60}},
					{"dimensions": []string{"2025-06-02"}, "metrics": []int64{200, 80, 90}},
				},
				"meta": map[string]int{"total_rows": 2},
			}
		} else {
			resp = map[string]interface{}{
				"results": []map[string]interface{}{
					{"dimensions": []string{"2025-06-01", "/docs"}, "metrics": []int64{40, 20, 25}},
					{"dimensions": []string{"2025-06-01", "/blog"}, "metrics": []int64{60, 30, 35}},
					{"dimensions": []string{"2025-06-02", "/docs"}, "metrics": []int64{80, 40, 45}},
				},
				"meta": map[string]int{"total_rows": 3},
			}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := &Plausible{
		Client:  server.Client(),
		APIKey:  "test-key",
		SiteID:  "example.com",
		BaseURL: server.URL,
	}

	records, err := p.Fetch(context.Background(), juneFetchOptions("mysite", 1, 2))
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	// Site: 2 days * 3 metrics = 6
	// Pages: 3 rows * 3 metrics = 9
	assertRecordCount(t, records, 15)

	sitePageviews := filterByMetric(records, "daily_site_pageviews")
	if len(sitePageviews) != 2 {
		t.Errorf("got %d site pageview records, want 2", len(sitePageviews))
	}

	siteVisitors := filterByMetric(records, "daily_site_visitors")
	if len(siteVisitors) != 2 {
		t.Errorf("got %d site visitor records, want 2", len(siteVisitors))
	}

	pagePageviews := filterByMetric(records, "daily_pageviews")
	if len(pagePageviews) != 3 {
		t.Errorf("got %d page pageview records, want 3", len(pagePageviews))
	}

	for _, r := range pagePageviews {
		if r.Extra["page"] == "" {
			t.Errorf("expected page tag, got empty")
		}
	}

	if callCount != 2 {
		t.Errorf("expected 2 API calls, got %d", callCount)
	}
}

func TestPlausiblePagination(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)

		dims, _ := body["dimensions"].([]interface{})
		hasTwoDims := len(dims) == 2

		if !hasTwoDims {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"results": []map[string]interface{}{
					{"dimensions": []string{"2025-06-01"}, "metrics": []int64{100, 50, 60}},
				},
				"meta": map[string]int{"total_rows": 1},
			})
			return
		}

		callCount++
		pagination, _ := body["pagination"].(map[string]interface{})
		offset := int(pagination["offset"].(float64))

		var resp map[string]interface{}
		if offset == 0 {
			resp = map[string]interface{}{
				"results": []map[string]interface{}{
					{"dimensions": []string{"2025-06-01", "/page1"}, "metrics": []int64{10, 5, 6}},
					{"dimensions": []string{"2025-06-01", "/page2"}, "metrics": []int64{20, 10, 12}},
				},
				"meta": map[string]int{"total_rows": 15000},
			}
		} else {
			resp = map[string]interface{}{
				"results": []map[string]interface{}{
					{"dimensions": []string{"2025-06-01", "/page3"}, "metrics": []int64{30, 15, 18}},
				},
				"meta": map[string]int{"total_rows": 15000},
			}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := &Plausible{
		Client:  server.Client(),
		APIKey:  "test-key",
		SiteID:  "example.com",
		BaseURL: server.URL,
	}

	records, err := p.Fetch(context.Background(), juneFetchOptions("mysite", 1, 1))
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	// Site: 1 day * 3 metrics = 3
	// Pages: 3 rows * 3 metrics = 9
	assertRecordCount(t, records, 12)

	pagePageviews := filterByMetric(records, "daily_pageviews")
	if len(pagePageviews) != 3 {
		t.Errorf("got %d page pageview records, want 3", len(pagePageviews))
	}

	if callCount != 2 {
		t.Errorf("expected 2 paginated API calls, got %d", callCount)
	}
}

// TestPlausiblePaginationIsStable verifies that paging through a breakdown
// returns every row exactly once. Without an explicit order, Plausible may sort
// each page differently, so rows repeat on one page and are missing from
// another.
func TestPlausiblePaginationIsStable(t *testing.T) {
	type row struct{ day, page string }
	var rows []row
	for _, day := range []string{"2025-06-01", "2025-06-02"} {
		for i := 0; i < 7500; i++ {
			rows = append(rows, row{day, fmt.Sprintf("/page%04d", i)})
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Dimensions []string       `json:"dimensions"`
			OrderBy    [][]string     `json:"order_by"`
			Pagination map[string]int `json:"pagination"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		if len(body.Dimensions) == 1 {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"results": []map[string]interface{}{
					{"dimensions": []string{"2025-06-01"}, "metrics": []int64{1, 1, 1}},
					{"dimensions": []string{"2025-06-02"}, "metrics": []int64{1, 1, 1}},
				},
				"meta": map[string]int{"total_rows": 2},
			})
			return
		}

		offset, limit := body.Pagination["offset"], body.Pagination["limit"]
		sorted := append([]row(nil), rows...)
		wantOrder := [][]string{{"time:day", "asc"}, {"event:page", "asc"}}
		if !reflect.DeepEqual(body.OrderBy, wantOrder) && offset > 0 {
			// Unordered: later pages come from a differently sorted result.
			sort.Slice(sorted, func(i, j int) bool { return sorted[i].page > sorted[j].page })
		}
		end := offset + limit
		if end > len(sorted) {
			end = len(sorted)
		}
		var results []map[string]interface{}
		for _, r := range sorted[offset:end] {
			results = append(results, map[string]interface{}{"dimensions": []string{r.day, r.page}, "metrics": []int64{1, 1, 1}})
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"results": results,
			"meta":    map[string]int{"total_rows": len(rows)},
		})
	}))
	defer server.Close()

	p := &Plausible{Client: server.Client(), APIKey: "test-key", SiteID: "example.com", BaseURL: server.URL}
	records, err := p.Fetch(context.Background(), juneFetchOptions("mysite", 1, 2))
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	seen := make(map[string]int)
	for _, rec := range filterByMetric(records, "daily_pageviews") {
		seen[rec.Date+" "+rec.Extra["page"]]++
	}
	for _, r := range rows {
		if n := seen[r.day+" "+r.page]; n != 1 {
			t.Fatalf("%s %s fetched %d times, want 1", r.day, r.page, n)
		}
	}
}

// TestPlausibleDateFilter verifies that rows with dates outside [StartDate,
// EndDate] or with unparseable dates are dropped client-side.
func TestPlausibleDateFilter(t *testing.T) {
	// The request window is 2025-06-01..2025-06-02.
	// The server returns: one in-range day, one before, one after, one bad date.
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var resp interface{}
		if callCount == 1 {
			// Site-level query: includes out-of-range and malformed dates.
			resp = map[string]interface{}{
				"results": []map[string]interface{}{
					{"dimensions": []string{"2025-06-01"}, "metrics": []int64{100, 50, 60}},
					{"dimensions": []string{"2025-05-31"}, "metrics": []int64{999, 1, 1}}, // before start
					{"dimensions": []string{"2025-06-03"}, "metrics": []int64{888, 2, 2}}, // after end
					{"dimensions": []string{"not-a-date"}, "metrics": []int64{777, 3, 3}}, // bad date
				},
				"meta": map[string]int{"total_rows": 4},
			}
		} else {
			// Page-level query: only in-range row survives.
			resp = map[string]interface{}{
				"results": []map[string]interface{}{
					{"dimensions": []string{"2025-06-02", "/index"}, "metrics": []int64{200, 80, 90}},
					{"dimensions": []string{"2025-05-30", "/old"}, "metrics": []int64{500, 10, 10}}, // before start
				},
				"meta": map[string]int{"total_rows": 2},
			}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p := &Plausible{
		Client:  server.Client(),
		APIKey:  "test-key",
		SiteID:  "example.com",
		BaseURL: server.URL,
	}

	records, err := p.Fetch(context.Background(), juneFetchOptions("mysite", 1, 2))
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	// Site: 1 in-range row * 3 metrics = 3
	// Pages: 1 in-range row * 3 metrics = 3
	// Total: 6
	assertRecordCount(t, records, 6)

	for _, r := range records {
		if r.Date != "2025-06-01" && r.Date != "2025-06-02" {
			t.Errorf("out-of-range record not filtered: date=%s metric=%s", r.Date, r.Metric)
		}
	}
}

func TestPlausibleAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	p := &Plausible{
		Client:  server.Client(),
		APIKey:  "bad-key",
		SiteID:  "example.com",
		BaseURL: server.URL,
	}

	_, err := p.Fetch(context.Background(), juneFetchOptions("mysite", 1, 1))
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
}

// A site's days end in its own time zone, which Plausible's Sites API
// reports. A configured zone overrides it, and without either the latest time
// zone there is, UTC-12, is assumed, so no partial day is ever stored.
func TestPlausibleDayZone(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/api/v1/sites/plotnine.org":
			_, _ = w.Write([]byte(`{"domain":"plotnine.org","timezone":"America/Los_Angeles"}`))
		case "/api/v1/sites/posit-dev.github.io/chatlas":
			_, _ = w.Write([]byte(`{"domain":"posit-dev.github.io/chatlas","timezone":"America/New_York"}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer server.Close()

	zone := func(p *Plausible) string {
		t.Helper()
		loc, err := p.DayZone(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		return loc.String()
	}

	p := &Plausible{Client: server.Client(), APIKey: "k", SiteID: "plotnine.org", BaseURL: server.URL}
	if got := zone(p); got != "America/Los_Angeles" {
		t.Errorf("zone = %s, want America/Los_Angeles", got)
	}
	_ = zone(p)
	if requests != 1 {
		t.Errorf("made %d Sites API requests for two lookups, want 1", requests)
	}

	if got := zone(&Plausible{Client: server.Client(), APIKey: "k", SiteID: "posit-dev.github.io/chatlas", BaseURL: server.URL}); got != "America/New_York" {
		t.Errorf("zone of a site with a path = %s, want America/New_York", got)
	}
	if got := zone(&Plausible{Client: server.Client(), APIKey: "k", SiteID: "plotnine.org", BaseURL: server.URL, TimeZone: "Europe/Amsterdam"}); got != "Europe/Amsterdam" {
		t.Errorf("configured zone = %s, want Europe/Amsterdam", got)
	}

	fallback, err := (&Plausible{Client: server.Client(), APIKey: "k", SiteID: "secret.example", BaseURL: server.URL}).DayZone(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, offset := time.Date(2026, 1, 15, 12, 0, 0, 0, fallback).Zone(); offset != -12*3600 {
		t.Errorf("fallback offset = %d, want UTC-12", offset)
	}
}
