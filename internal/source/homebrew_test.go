package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHomebrewFetch(t *testing.T) {
	server := httptest.NewServer(jsonHandler(t, http.StatusOK, map[string]interface{}{
		"analytics": map[string]interface{}{
			"install": map[string]interface{}{
				"30d": map[string]int64{
					"wget":        22297,
					"wget --HEAD": 33,
				},
				"90d": map[string]int64{
					"wget":        74263,
					"wget --HEAD": 159,
				},
				"365d": map[string]int64{
					"wget":        382652,
					"wget --HEAD": 722,
				},
			},
		},
	}))
	defer server.Close()

	h := &Homebrew{
		Client:  server.Client(),
		Formula: "wget",
		BaseURL: server.URL,
	}

	records, err := h.Fetch(context.Background(), juneFetchOptions("wget", 1, 1))
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	assertRecordCount(t, records, 3)

	for _, r := range records {
		if r.ProjectID != "wget" {
			t.Errorf("expected project_id=wget, got %s", r.ProjectID)
		}
		if r.Date != "2025-06-01" {
			t.Errorf("expected date=2025-06-01 (opts.EndDate), got %s", r.Date)
		}
	}

	assertMetricValues(t, records, map[string]int64{
		"downloads_30d":  22330,
		"downloads_90d":  74422,
		"downloads_365d": 383374,
	})
}

func TestHomebrewAPIError(t *testing.T) {
	server := errorServer(t, http.StatusNotFound)

	h := &Homebrew{
		Client:  server.Client(),
		Formula: "nonexistent",
		BaseURL: server.URL,
	}

	_, err := h.Fetch(context.Background(), juneFetchOptions("nonexistent", 1, 1))
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}
