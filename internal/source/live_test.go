//go:build live

// Live tests fetch from the real APIs, so a change in an API is noticed by
// velocirepo's own CI before it reaches anyone's data. They only run with
// the build tag: go test -tags live ./internal/source -run Live
package source

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func liveClient() *http.Client { return &http.Client{Timeout: 60 * time.Second} }

// liveSecret returns a secret, or skips the test with the reason, so that a
// missing secret is never mistaken for a pass.
func liveSecret(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("skipped: %s isn't set", name)
	}
	return v
}

func liveWindow(days int) FetchOptions {
	end := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -3)
	return FetchOptions{ProjectID: "live", StartDate: end.AddDate(0, 0, -days), EndDate: end}
}

func requireRecords(t *testing.T, records []Record, metric string) {
	t.Helper()
	for _, r := range records {
		if r.Metric == metric {
			if r.Value < 0 {
				t.Errorf("%s has a negative value: %+v", metric, r)
			}
			return
		}
	}
	t.Errorf("no %s records among %d records", metric, len(records))
}

func TestLiveGitHub(t *testing.T) {
	token := liveSecret(t, "GITHUB_TOKEN")
	g := &GitHubEvents{Client: liveClient(), Token: token, Repo: "posit-dev/velocirepo"}
	opts := liveWindow(30)
	opts.Windows = map[string]Window{"stars": {End: opts.EndDate}}
	if _, err := g.FetchEvents(context.Background(), opts); err != nil {
		t.Fatalf("FetchEvents: %v", err)
	}
	requireRecords(t, g.Records(), "total_stars")
	requireRecords(t, g.Records(), "daily_stars")
}

// GitHub's star history returns weeks of seven days, newest first, at most 30
// per page and 100 pages.
func TestLiveGitHubStarHistoryShape(t *testing.T) {
	token := liveSecret(t, "GITHUB_TOKEN")
	g := &GitHubEvents{Client: liveClient(), Token: token, Repo: "tidyverse/ggplot2"}
	var weeks []struct {
		Week int64   `json:"week"`
		Days []int64 `json:"days"`
	}
	if err := g.getREST(context.Background(), "https://api.github.com/repos/tidyverse/ggplot2/stargazers/history?per_page=30&page=1", &weeks); err != nil {
		t.Fatal(err)
	}
	if len(weeks) != 30 {
		t.Errorf("got %d weeks, want 30", len(weeks))
	}
	for i, w := range weeks {
		if len(w.Days) != 7 {
			t.Errorf("week %d has %d days", i, len(w.Days))
		}
		if i > 0 && w.Week >= weeks[i-1].Week {
			t.Errorf("weeks aren't newest first at %d", i)
		}
	}
	err := g.getREST(context.Background(), "https://api.github.com/repos/tidyverse/ggplot2/stargazers/history?per_page=30&page=101", &weeks)
	if err == nil || !strings.Contains(err.Error(), "422") {
		t.Errorf("page 101 = %v, want GitHub's 100-page limit (422)", err)
	}
}

// GitHub serves at most 300 pages of 100 of a repository's comments; the
// comment listing depends on that limit.
func TestLiveGitHubCommentPageLimit(t *testing.T) {
	token := liveSecret(t, "GITHUB_TOKEN")
	g := &GitHubEvents{Client: liveClient(), Token: token, Repo: "posit-dev/positron"}
	var comments []map[string]any
	url := fmt.Sprintf("https://api.github.com/repos/posit-dev/positron/issues/comments?sort=updated&direction=asc&per_page=100&page=%d", commentPageLimit+1)
	err := g.getREST(context.Background(), url, &comments)
	if err == nil || !strings.Contains(err.Error(), "422") {
		t.Errorf("page %d = %v, want GitHub's page limit (422)", commentPageLimit+1, err)
	}
}

func TestLiveGitHubTraffic(t *testing.T) {
	token := liveSecret(t, "TRAFFIC_TOKEN")
	records, err := (&GitHubTraffic{Client: liveClient(), Token: token, Repo: "posit-dev/velocirepo"}).Fetch(context.Background(), liveWindow(7))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) == 0 {
		t.Error("no traffic records")
	}
}

func TestLiveCRAN(t *testing.T) {
	c := &CRAN{Client: liveClient(), Package: "dplyr"}
	records, err := c.Fetch(context.Background(), liveWindow(7))
	if err != nil {
		t.Fatal(err)
	}
	requireRecords(t, records, "daily_downloads")
	published, err := c.PublishedThrough(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if age := time.Since(published); age > 5*24*time.Hour {
		t.Errorf("cranlogs' last day is %s, more than five days ago", published.Format("2006-01-02"))
	}
}

func TestLivePyPI(t *testing.T) {
	p := &PyPI{Client: liveClient(), Package: "great-tables"}
	records, err := p.Fetch(context.Background(), liveWindow(7))
	if err != nil {
		t.Fatal(err)
	}
	requireRecords(t, records, "daily_downloads")
	if published, err := p.PublishedThrough(context.Background()); err != nil || time.Since(published) > 5*24*time.Hour {
		t.Errorf("pypistats' last day = %v, %v", published, err)
	}
}

func TestLiveHomebrew(t *testing.T) {
	records, err := (&Homebrew{Client: liveClient(), Formula: "jq"}).Fetch(context.Background(), liveWindow(1))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) == 0 {
		t.Error("no Homebrew records")
	}
}

func TestLiveOpenVSX(t *testing.T) {
	records, err := (&OpenVSX{Client: liveClient(), ExtensionID: "posit/air-vscode"}).Fetch(context.Background(), liveWindow(1))
	if err != nil {
		t.Fatal(err)
	}
	requireRecords(t, records, "total_downloads")
}

func TestLiveRSS(t *testing.T) {
	r := &RSS{Client: liveClient(), FeedURL: "https://github.blog/changelog/feed/"}
	if _, err := r.Fetch(context.Background(), liveWindow(30)); err != nil {
		t.Fatal(err)
	}
	if len(r.ContentEntries()) == 0 {
		t.Error("the GitHub changelog feed has no items")
	}
}

// The Sites API gives a site's time zone; without access, velocirepo falls
// back to UTC-12.
func TestLivePlausible(t *testing.T) {
	token := liveSecret(t, "PLAUSIBLE_TOKEN")
	site := liveSecret(t, "PLAUSIBLE_TEST_SITE")
	p := &Plausible{Client: liveClient(), APIKey: token, SiteID: site}
	records, err := p.Fetch(context.Background(), liveWindow(7))
	if err != nil {
		t.Fatal(err)
	}
	requireRecords(t, records, "daily_site_pageviews")
	if zone, err := p.DayZone(context.Background(), ""); err != nil || zone == latestTimeZone {
		t.Errorf("site's time zone = %v, %v; want one from the Sites API", zone, err)
	}
}

func TestLiveYouTube(t *testing.T) {
	key := liveSecret(t, "YOUTUBE_TOKEN")
	records, err := (&YouTube{Client: liveClient(), APIKey: key, Target: "@PositPBC"}).Fetch(context.Background(), liveWindow(1))
	if err != nil {
		t.Fatal(err)
	}
	requireRecords(t, records, "total_subscribers")
}

func TestLiveLinkedIn(t *testing.T) {
	t.Skip("skipped: LinkedIn has no public target, and its tokens expire after 60 days")
}

// The reference endpoints, so a change in their shape is noticed as a
// broken reference, not as a mismatch in the data.
func TestLiveReferenceTotals(t *testing.T) {
	from, through := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	t.Run("github", func(t *testing.T) {
		g := &GitHubEvents{Client: liveClient(), Token: liveSecret(t, "GITHUB_TOKEN")}
		totals, err := g.ReferenceTotals(context.Background(), []string{"posit-dev/velocirepo", "tidyverse/ggplot2"})
		if err != nil || len(totals) != 10 {
			t.Fatalf("totals = %+v, %v", totals, err)
		}
		monthly, err := g.MonthlyReferenceTotals(context.Background(), []string{"tidyverse/ggplot2"}, from, through, nil)
		if err != nil || len(monthly) != 4 || monthly[0].Unavailable != "" {
			t.Errorf("monthly = %+v, %v", monthly, err)
		}
	})
	t.Run("cran", func(t *testing.T) {
		totals, err := (&CRAN{Client: liveClient()}).MonthlyReferenceTotals(context.Background(), []string{"dplyr"}, from, through, nil)
		if err != nil || len(totals) != 1 || totals[0].Value == 0 {
			t.Errorf("totals = %+v, %v", totals, err)
		}
	})
	t.Run("plausible", func(t *testing.T) {
		p := &Plausible{Client: liveClient(), APIKey: liveSecret(t, "PLAUSIBLE_TOKEN")}
		totals, err := p.MonthlyReferenceTotals(context.Background(), []string{liveSecret(t, "PLAUSIBLE_TEST_SITE")}, from, through, nil)
		if err != nil || len(totals) != 2 || totals[0].Unavailable != "" {
			t.Errorf("totals = %+v, %v", totals, err)
		}
	})
}
