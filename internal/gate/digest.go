package gate

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/posit-dev/velocirepo/internal/store"
)

// DigestLabel marks the long-lived issue the weekly digest is posted on.
const DigestLabel = "velocirepo-digest"

const digestTitle = "Weekly digest"

// digestWeeks is how many weeks the sparklines cover.
const digestWeeks = 8

// digestMetrics are the metric each source's sparkline shows: a daily
// metric is summed over the week, a total_* metric shows the week's highest
// value. GitHub shows its events.
var digestMetrics = map[string]string{
	"pypi":           "daily_downloads",
	"cran":           "daily_downloads",
	"plausible":      "daily_site_pageviews",
	"github-traffic": "daily_views",
	"homebrew":       "downloads_30d",
	"openvsx":        "total_downloads",
	"youtube":        "total_views",
	"rss":            "total_items",
	"linkedin":       "total_impressions",
}

// DigestRow is one source and project's weekly values, oldest first.
type DigestRow struct {
	Source  string
	Project string
	Metric  string
	Weeks   []int64
}

// digestStart returns the Monday that starts the first of the weeks that
// end with the last complete week before now.
func digestStart(now time.Time) time.Time {
	d := now.UTC().Truncate(24 * time.Hour)
	for d.Weekday() != time.Monday {
		d = d.AddDate(0, 0, -1)
	}
	return d.AddDate(0, 0, -7*digestWeeks)
}

// DigestRows computes the sparklines' values from the stored data.
func DigestRows(dataDir string, now time.Time) ([]DigestRow, time.Time, error) {
	start := digestStart(now)
	end := start.AddDate(0, 0, 7*digestWeeks)
	var values []string
	for src, metric := range digestMetrics {
		values = append(values, fmt.Sprintf("('%s', '%s')", src, metric))
	}
	sort.Strings(values)
	period := fmt.Sprintf("DATE '%s' AND DATE '%s'", start.Format("2006-01-02"), end.AddDate(0, 0, -1).Format("2006-01-02"))
	query := fmt.Sprintf(`
		WITH chosen(source, metric) AS (VALUES %s),
		daily AS (
			SELECT m.source, m.project, m.metric, m.date, m.value
			FROM metrics m JOIN chosen c ON m.source = c.source AND m.metric = c.metric
			WHERE m.date BETWEEN %s
		)
		SELECT source, project, metric,
			CAST(datediff('day', DATE '%s', date) // 7 AS INTEGER) AS week,
			CASE WHEN metric LIKE 'total_%%' OR metric LIKE 'downloads_%%' THEN max(value) ELSE sum(value) END AS value
		FROM daily GROUP BY 1, 2, 3, 4`, strings.Join(values, ", "), period, start.Format("2006-01-02"))
	rows, _, err := store.QueryLive(dataDir, nil, nil, query)
	if err != nil && !strings.Contains(err.Error(), "does not exist") {
		return nil, start, err
	}
	byKey := map[string]*DigestRow{}
	add := func(src, project, metric string, week int, value int64) {
		key := src + "/" + project
		row, ok := byKey[key]
		if !ok {
			row = &DigestRow{Source: src, Project: project, Metric: metric, Weeks: make([]int64, digestWeeks)}
			byKey[key] = row
		}
		if week >= 0 && week < digestWeeks {
			row.Weeks[week] = value
		}
	}
	for _, r := range rows {
		add(str(r["source"]), str(r["project"]), str(r["metric"]), int(num(r["week"])), num(r["value"]))
	}
	events, _, err := store.QueryLive(dataDir, nil, nil, fmt.Sprintf(`
		SELECT source, project, CAST(datediff('day', DATE '%s', CAST(datetime AS DATE)) // 7 AS INTEGER) AS week, count(*) AS value
		FROM events WHERE CAST(datetime AS DATE) BETWEEN %s GROUP BY ALL`, start.Format("2006-01-02"), period))
	if err != nil && !strings.Contains(err.Error(), "does not exist") {
		return nil, start, err
	}
	for _, r := range events {
		add(str(r["source"]), str(r["project"]), "events", int(num(r["week"])), num(r["value"]))
	}
	out := make([]DigestRow, 0, len(byKey))
	for _, row := range byKey {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Project < out[j].Project
	})
	return out, start, nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	case fmt.Stringer:
		var x int64
		_, _ = fmt.Sscan(n.String(), &x)
		return x
	}
	return 0
}

var sparks = []rune("▁▂▃▄▅▆▇█")

// sparkline draws weekly values; a week without data is a dot, so weeks of
// nothing stand out.
func sparkline(values []int64) string {
	var hi int64
	for _, v := range values {
		hi = max(hi, v)
	}
	var b strings.Builder
	for _, v := range values {
		switch {
		case v <= 0:
			b.WriteRune('·')
		case hi == 0:
			b.WriteRune(sparks[0])
		default:
			b.WriteRune(sparks[int((v*int64(len(sparks)-1)+hi/2)/hi)])
		}
	}
	return b.String()
}

// stopped reports whether a row usually had data, in at least four of the
// earlier weeks, and has none for the last two: quiet projects with the odd
// active week aren't marked.
func (r DigestRow) stopped() bool {
	n := len(r.Weeks)
	if n < 3 || r.Weeks[n-1] > 0 || r.Weeks[n-2] > 0 {
		return false
	}
	active := 0
	for _, v := range r.Weeks[:n-2] {
		if v > 0 {
			active++
		}
	}
	return active >= 4
}

// RenderDigest renders the digest: the week's findings, then a sparkline
// per source and project, the ones that stopped first.
func RenderDigest(rows []DigestRow, start time.Time, state State, now time.Time) string {
	var b strings.Builder
	end := start.AddDate(0, 0, 7*digestWeeks-1)
	fmt.Fprintf(&b, "## Week of %s\n\n", end.AddDate(0, 0, -6).Format("2006-01-02"))

	weekAgo := now.AddDate(0, 0, -7).Format("2006-01-02")
	var recent []OpenFinding
	for _, f := range state.Findings {
		if f.Since >= weekAgo {
			recent = append(recent, f)
		}
	}
	fmt.Fprintf(&b, "%d finding(s) open, %d of them new this week.\n\n", len(state.Findings), len(recent))
	for _, f := range recent {
		fmt.Fprintf(&b, "- %s `%s` %s: %s\n", icon(f.Severity), f.Check, strings.Trim(f.Source+"/"+f.Project, "/"), f.Message)
	}
	if len(recent) > 0 {
		b.WriteString("\n")
	}

	sorted := append([]DigestRow(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].stopped() && !sorted[j].stopped() })
	fmt.Fprintf(&b, "### %s to %s\n\nOne line per source and project; a dot is a week without data.\n\n", start.Format("2006-01-02"), end.Format("2006-01-02"))
	b.WriteString("| | Source | Project | Weekly | Last week |\n|---|---|---|---|---|\n")
	for _, r := range sorted {
		mark := ""
		if r.stopped() {
			mark = "⚠️"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | `%s` | %s %s |\n", mark, r.Source, r.Project, sparkline(r.Weeks), thousandsInt(r.Weeks[len(r.Weeks)-1]), r.Metric)
	}
	return b.String()
}

func thousandsInt(n int64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0 && s[i-1] != '-'; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// PostDigest comments the digest on its own long-lived issue, opening it the
// first time. It never touches the tracking issue.
func PostDigest(ctx context.Context, api githubAPI, body string) error {
	var open []issue
	if _, err := api.call(ctx, "list issues", "issues: write", http.MethodGet, "/issues?state=open&per_page=1&labels="+DigestLabel, nil, &open); err != nil {
		return err
	}
	if len(open) == 0 {
		in := map[string]any{"title": digestTitle, "body": "velocirepo posts a digest of the data here every week, for the problems no check was written for.", "labels": []string{DigestLabel}}
		var created issue
		if _, err := api.call(ctx, "open the digest issue", "issues: write", http.MethodPost, "/issues", in, &created); err != nil {
			return err
		}
		open = append(open, created)
	}
	_, err := api.call(ctx, "post the digest", "issues: write", http.MethodPost, fmt.Sprintf("/issues/%d/comments", open[0].Number), map[string]any{"body": body}, nil)
	return err
}

// NewDigestAPI returns the API the digest is posted with, using the
// workflow token.
func NewDigestAPI(repo, token, apiURL string) githubAPI {
	return githubAPI{Token: token, Repo: repo, URL: apiURL}
}

// ReadState reads the gate's state from its file.
func ReadState(path string) (State, error) { return readState(path) }
