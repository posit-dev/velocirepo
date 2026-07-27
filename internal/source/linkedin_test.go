package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLinkedInFetchOrganization(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/rest/posts", func(w http.ResponseWriter, r *http.Request) {
		assertBearerToken(t, r, "test-token")
		if got := r.Header.Get("LinkedIn-Version"); got != "202406" {
			t.Errorf("LinkedIn-Version = %q, want %q", got, "202406")
		}

		_ = json.NewEncoder(w).Encode(postsResponse{
			Elements: []linkedinPost{
				{
					ID:         "urn:li:share:7123456789012345678",
					Author:     "urn:li:organization:12345678",
					CreatedAt:  1719792000000,
					Commentary: "Excited to announce our new release! #opensource #golang",
					Visibility: "PUBLIC",
					Lifecycle:  "PUBLISHED",
				},
				{
					ID:         "urn:li:share:7198765432109876543",
					Author:     "urn:li:organization:12345678",
					CreatedAt:  1719360000000,
					Commentary: "Check out our latest blog post",
					Content:    &linkedinContent{Article: &linkedinArticle{Source: "https://example.com/blog", Title: "Blog Post"}},
					Visibility: "PUBLIC",
					Lifecycle:  "PUBLISHED",
				},
			},
			Paging: struct {
				Count int `json:"count"`
				Start int `json:"start"`
				Total int `json:"total"`
			}{Count: 100, Start: 0, Total: 2},
		})
	})

	mux.HandleFunc("/rest/organizationalEntityShareStatistics", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(statsResponse{
			Elements: []statsElement{
				{
					Stats: shareStats{ImpressionCount: 4521, LikeCount: 85, CommentCount: 14, ShareCount: 12, ClickCount: 234},
					Share: "urn:li:share:7123456789012345678",
				},
				{
					Stats: shareStats{ImpressionCount: 2000, LikeCount: 40, CommentCount: 5, ShareCount: 3, ClickCount: 100},
					Share: "urn:li:share:7198765432109876543",
				},
			},
		})
	})

	mux.HandleFunc("/rest/networkSizes/", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(followerResponse{FirstDegreeSize: 15420})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	li := &LinkedIn{
		Client:  srv.Client(),
		Token:   "test-token",
		Target:  "urn:li:organization:12345678",
		BaseURL: srv.URL,
	}

	records, err := li.Fetch(context.Background(), juneFetchOptions("my-project", 1, 30))
	if err != nil {
		t.Fatal(err)
	}

	// 5 metrics per post * 2 posts + 1 follower count = 11
	assertRecordCount(t, records, 11)

	// Verify first post metrics
	if records[0].Metric != "total_impressions" {
		t.Errorf("expected total_impressions, got %s", records[0].Metric)
	}
	if records[0].Value != 4521 {
		t.Errorf("expected 4521, got %d", records[0].Value)
	}
	if records[0].Extra["post_id"] != "urn:li:share:7123456789012345678" {
		t.Errorf("unexpected post_id tag: %v", records[0].Extra)
	}

	// Verify follower count (last record)
	last := records[10]
	if last.Metric != "total_followers" {
		t.Errorf("expected total_followers, got %s", last.Metric)
	}
	if last.Value != 15420 {
		t.Errorf("expected 15420, got %d", last.Value)
	}

	// Verify content entries
	entries := li.ContentEntries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 content entries, got %d", len(entries))
	}

	if entries[0].ID != "urn:li:share:7123456789012345678" {
		t.Errorf("unexpected ID: %s", entries[0].ID)
	}
	if entries[0].Type != "post" {
		t.Errorf("expected type=post, got %s", entries[0].Type)
	}
	if len(entries[0].Tags) != 2 || entries[0].Tags[0] != "opensource" || entries[0].Tags[1] != "golang" {
		t.Errorf("unexpected tags: %v", entries[0].Tags)
	}

	if entries[1].Type != "article" {
		t.Errorf("expected type=article, got %s", entries[1].Type)
	}
	if entries[1].Extra["article_url"] != "https://example.com/blog" {
		t.Errorf("unexpected metadata: %v", entries[1].Extra)
	}
}

func TestLinkedInFetchPagination(t *testing.T) {
	callCount := 0
	mux := http.NewServeMux()

	mux.HandleFunc("/rest/posts", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		start := r.URL.Query().Get("start")

		if start == "0" || start == "" {
			_ = json.NewEncoder(w).Encode(postsResponse{
				Elements: []linkedinPost{
					{ID: "urn:li:share:1", CreatedAt: 1719792000000, Commentary: "Post 1"},
					{ID: "urn:li:share:2", CreatedAt: 1719791000000, Commentary: "Post 2"},
				},
				Paging: struct {
					Count int `json:"count"`
					Start int `json:"start"`
					Total int `json:"total"`
				}{Count: 2, Start: 0, Total: 3},
			})
		} else {
			_ = json.NewEncoder(w).Encode(postsResponse{
				Elements: []linkedinPost{
					{ID: "urn:li:share:3", CreatedAt: 1719790000000, Commentary: "Post 3"},
				},
				Paging: struct {
					Count int `json:"count"`
					Start int `json:"start"`
					Total int `json:"total"`
				}{Count: 2, Start: 2, Total: 3},
			})
		}
	})

	mux.HandleFunc("/rest/organizationalEntityShareStatistics", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(statsResponse{Elements: []statsElement{}})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	li := &LinkedIn{
		Client:  srv.Client(),
		Token:   "test-token",
		Target:  "urn:li:person:abc",
		BaseURL: srv.URL,
	}

	_, err := li.Fetch(context.Background(), juneFetchOptions("proj", 1, 30))
	if err != nil {
		t.Fatal(err)
	}

	if callCount != 2 {
		t.Errorf("expected 2 page requests, got %d", callCount)
	}

	entries := li.ContentEntries()
	if len(entries) != 3 {
		t.Fatalf("expected 3 content entries, got %d", len(entries))
	}
}

func TestLinkedInPersonalNoFollowers(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/rest/posts", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(postsResponse{
			Elements: []linkedinPost{
				{ID: "urn:li:share:1", CreatedAt: 1719792000000, Commentary: "Hello"},
			},
			Paging: struct {
				Count int `json:"count"`
				Start int `json:"start"`
				Total int `json:"total"`
			}{Count: 100, Start: 0, Total: 1},
		})
	})

	mux.HandleFunc("/rest/organizationalEntityShareStatistics", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(statsResponse{
			Elements: []statsElement{
				{Stats: shareStats{ImpressionCount: 100, LikeCount: 5}, Share: "urn:li:share:1"},
			},
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	li := &LinkedIn{
		Client:  srv.Client(),
		Token:   "test-token",
		Target:  "urn:li:person:Ab1CdEfGhI",
		BaseURL: srv.URL,
	}

	records, err := li.Fetch(context.Background(), juneFetchOptions("proj", 1, 30))
	if err != nil {
		t.Fatal(err)
	}

	// 5 metrics for 1 post, no follower count (personal profile)
	assertRecordCount(t, records, 5)
}

func TestLinkedInTitleTruncation(t *testing.T) {
	long := "This is a very long commentary that exceeds one hundred characters and should be truncated at a word boundary somewhere around here"
	entry := postToContentEntry(linkedinPost{
		ID:         "urn:li:share:1",
		CreatedAt:  1719792000000,
		Commentary: long,
	}, "urn:li:organization:1")

	if len(entry.Title) > 110 {
		t.Errorf("title too long: %d chars", len(entry.Title))
	}
	if entry.Title[len(entry.Title)-3:] != "..." {
		t.Errorf("expected title to end with ..., got %q", entry.Title[len(entry.Title)-5:])
	}
	if entry.Description != long {
		t.Error("description should be full commentary")
	}
}

func TestLinkedInAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Expired token"}`))
	}))
	defer srv.Close()

	li := &LinkedIn{
		Client:  srv.Client(),
		Token:   "expired",
		Target:  "urn:li:organization:1",
		BaseURL: srv.URL,
	}

	_, err := li.Fetch(context.Background(), juneFetchOptions("proj", 1, 30))
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
	if got := err.Error(); got == "" {
		t.Error("expected non-empty error message")
	}
}
