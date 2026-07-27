package source

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/posit-dev/velocirepo/internal/dateutil"
)

type LinkedIn struct {
	Client  *http.Client
	Token   string
	Target  string
	BaseURL string

	contentEntries []ContentEntry
}

func (l *LinkedIn) Name() string { return "linkedin" }

func (l *LinkedIn) ContentEntries() []ContentEntry {
	return l.contentEntries
}

func (l *LinkedIn) ContentFilename() string {
	return "posts.jsonl"
}

func (l *LinkedIn) Fetch(ctx context.Context, opts FetchOptions) ([]Record, error) {
	l.contentEntries = nil

	posts, err := l.fetchPosts(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch posts: %w", err)
	}

	for _, p := range posts {
		l.contentEntries = append(l.contentEntries, postToContentEntry(p, l.Target))
	}

	var records []Record
	date := dateutil.FormatDate(opts.EndDate)

	if len(posts) > 0 {
		statsRecords, err := l.fetchPostStats(ctx, opts.ProjectID, date, posts)
		if err != nil {
			return nil, fmt.Errorf("fetch stats: %w", err)
		}
		records = append(records, statsRecords...)
	}

	if strings.Contains(l.Target, "organization") {
		followerRecords, err := l.fetchFollowerCount(ctx, opts.ProjectID, date)
		if err == nil {
			records = append(records, followerRecords...)
		}
	}

	return records, nil
}

func (l *LinkedIn) fetchPosts(ctx context.Context) ([]linkedinPost, error) {
	var allPosts []linkedinPost
	start := 0

	for {
		u := fmt.Sprintf("%s/rest/posts?author=%s&count=100&start=%d&sortBy=LAST_MODIFIED",
			l.baseURL(), url.QueryEscape(l.Target), start)

		var resp postsResponse
		if err := l.get(ctx, u, &resp); err != nil {
			return nil, err
		}

		allPosts = append(allPosts, resp.Elements...)

		if len(resp.Elements) == 0 || len(resp.Elements) < resp.Paging.Count || start+len(resp.Elements) >= resp.Paging.Total {
			break
		}
		start += len(resp.Elements)
	}

	return allPosts, nil
}

func (l *LinkedIn) fetchPostStats(ctx context.Context, projectID, date string, posts []linkedinPost) ([]Record, error) {
	var records []Record

	for i := 0; i < len(posts); i += 20 {
		end := i + 20
		if end > len(posts) {
			end = len(posts)
		}
		batch := posts[i:end]

		var urns []string
		for _, p := range batch {
			urns = append(urns, p.ID)
		}
		sharesParam := "List(" + strings.Join(urns, ",") + ")"

		u := fmt.Sprintf("%s/rest/organizationalEntityShareStatistics?organizationalEntity=%s&shares=%s",
			l.baseURL(), url.QueryEscape(l.Target), url.QueryEscape(sharesParam))

		var resp statsResponse
		if err := l.get(ctx, u, &resp); err != nil {
			return nil, err
		}

		for _, stat := range resp.Elements {
			records = append(records, linkedinShareStatRecords(projectID, l.Target, date, stat)...)
		}
	}

	return records, nil
}

var linkedinShareMetrics = []struct {
	name  string
	value func(shareStats) int64
}{
	{"total_impressions", func(s shareStats) int64 { return s.ImpressionCount }},
	{"total_likes", func(s shareStats) int64 { return s.LikeCount }},
	{"total_comments", func(s shareStats) int64 { return s.CommentCount }},
	{"total_shares", func(s shareStats) int64 { return s.ShareCount }},
	{"total_clicks", func(s shareStats) int64 { return s.ClickCount }},
}

func linkedinShareStatRecords(projectID, target, date string, stat statsElement) []Record {
	records := make([]Record, 0, len(linkedinShareMetrics))
	tags := map[string]string{"post_id": stat.Share}
	for _, metric := range linkedinShareMetrics {
		records = append(records, Record{
			Source:    "linkedin",
			Metric:    metric.name,
			ProjectID: projectID,
			Target:    target,
			Date:      date,
			Value:     metric.value(stat.Stats),
			Extra:     copyTags(tags),
		})
	}
	return records
}

func (l *LinkedIn) fetchFollowerCount(ctx context.Context, projectID, date string) ([]Record, error) {
	u := fmt.Sprintf("%s/rest/networkSizes/%s?edgeType=COMPANY_FOLLOWED_BY_MEMBER",
		l.baseURL(), url.QueryEscape(l.Target))

	var resp followerResponse
	if err := l.get(ctx, u, &resp); err != nil {
		return nil, err
	}

	return []Record{
		{Source: "linkedin", Metric: "total_followers", ProjectID: projectID, Target: l.Target, Date: date, Value: resp.FirstDegreeSize},
	}, nil
}

func (l *LinkedIn) get(ctx context.Context, u string, result interface{}) error {
	return doJSONInto(ctx, l.Client, httpJSONRequest{
		URL: u,
		Headers: map[string]string{
			"Authorization":             "Bearer " + l.Token,
			"LinkedIn-Version":          "202406",
			"X-Restli-Protocol-Version": "2.0.0",
		},
		RequestError: "request",
		StatusError:  "linkedin API returned",
	}, result)
}

func (l *LinkedIn) baseURL() string {
	if l.BaseURL != "" {
		return l.BaseURL
	}
	return "https://api.linkedin.com"
}

func postToContentEntry(p linkedinPost, target string) ContentEntry {
	publishedAt := time.UnixMilli(p.CreatedAt).UTC().Format(time.RFC3339)

	title := p.Commentary
	if len(title) > 100 {
		title = title[:100]
		if idx := strings.LastIndex(title, " "); idx > 50 {
			title = title[:idx]
		}
		title += "..."
	}

	var tags []string
	for _, match := range hashtagRe.FindAllString(p.Commentary, -1) {
		tags = append(tags, strings.TrimPrefix(match, "#"))
	}

	postType := "post"
	var metadata map[string]any
	if p.Content != nil {
		if p.Content.Article != nil {
			postType = "article"
			metadata = map[string]any{"article_url": p.Content.Article.Source}
		} else if p.Content.Media != nil {
			postType = "media"
		}
	}

	return ContentEntry{
		Source:      "linkedin",
		Target:      target,
		ID:          p.ID,
		Title:       title,
		Description: p.Commentary,
		PublishedAt: publishedAt,
		URL:         "https://www.linkedin.com/feed/update/" + p.ID,
		Tags:        tags,
		Type:        postType,
		Extra:       metadata,
	}
}

var hashtagRe = regexp.MustCompile(`#\w+`)

// API response types

type linkedinPost struct {
	ID         string           `json:"id"`
	Author     string           `json:"author"`
	CreatedAt  int64            `json:"createdAt"`
	Commentary string           `json:"commentary"`
	Content    *linkedinContent `json:"content"`
	Visibility string           `json:"visibility"`
	Lifecycle  string           `json:"lifecycleState"`
}

type linkedinContent struct {
	Article *linkedinArticle `json:"article"`
	Media   *linkedinMedia   `json:"media"`
}

type linkedinArticle struct {
	Source      string `json:"source"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type linkedinMedia struct {
	ID string `json:"id"`
}

type postsResponse struct {
	Elements []linkedinPost `json:"elements"`
	Paging   struct {
		Count int `json:"count"`
		Start int `json:"start"`
		Total int `json:"total"`
	} `json:"paging"`
}

type shareStats struct {
	ImpressionCount int64   `json:"impressionCount"`
	LikeCount       int64   `json:"likeCount"`
	CommentCount    int64   `json:"commentCount"`
	ShareCount      int64   `json:"shareCount"`
	ClickCount      int64   `json:"clickCount"`
	Engagement      float64 `json:"engagement"`
}

type statsElement struct {
	Stats shareStats `json:"totalShareStatistics"`
	Share string     `json:"share"`
}

type statsResponse struct {
	Elements []statsElement `json:"elements"`
}

type followerResponse struct {
	FirstDegreeSize int64 `json:"firstDegreeSize"`
}
