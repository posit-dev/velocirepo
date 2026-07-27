package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
