package source

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/posit-dev/velocirepo/internal/dateutil"
)

type YouTubeIndexEntry struct {
	VideoID     string   `json:"video_id"`
	Title       string   `json:"title"`
	PublishedAt string   `json:"published_at"`
	Channel     string   `json:"channel"`
	Duration    int64    `json:"duration"`
	Tags        []string `json:"tags,omitempty"`
}

type YouTube struct {
	Client  *http.Client
	APIKey  string
	Target  string
	BaseURL string

	contentEntries []ContentEntry
}

func (y *YouTube) Name() string { return "youtube" }

func (y *YouTube) ContentEntries() []ContentEntry {
	return y.contentEntries
}

func (y *YouTube) ContentFilename() string {
	return "videos.jsonl"
}

func (y *YouTube) Fetch(ctx context.Context, opts FetchOptions) ([]Record, error) {
	y.contentEntries = nil

	targetType := detectYouTubeType(y.Target)
	switch targetType {
	case "channel":
		return y.fetchChannel(ctx, opts)
	case "playlist":
		return y.fetchPlaylist(ctx, opts)
	case "video":
		return y.fetchVideos(ctx, opts, []string{y.Target})
	default:
		return nil, fmt.Errorf("cannot determine YouTube target type for %q", y.Target)
	}
}

func (y *YouTube) fetchChannel(ctx context.Context, opts FetchOptions) ([]Record, error) {
	channelID, err := y.resolveChannelID(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve channel: %w", err)
	}

	var records []Record

	channelRecords, err := y.fetchChannelStats(ctx, opts, channelID)
	if err != nil {
		return nil, fmt.Errorf("channel stats: %w", err)
	}
	records = append(records, channelRecords...)

	uploadsPlaylistID := "UU" + channelID[2:]
	videoIDs, err := y.fetchPlaylistVideoIDs(ctx, uploadsPlaylistID)
	if err != nil {
		return nil, fmt.Errorf("list videos: %w", err)
	}

	videoRecords, err := y.fetchVideos(ctx, opts, videoIDs)
	if err != nil {
		return nil, fmt.Errorf("video stats: %w", err)
	}
	records = append(records, videoRecords...)

	return records, nil
}

func (y *YouTube) fetchPlaylist(ctx context.Context, opts FetchOptions) ([]Record, error) {
	videoIDs, err := y.fetchPlaylistVideoIDs(ctx, y.Target)
	if err != nil {
		return nil, fmt.Errorf("list playlist videos: %w", err)
	}

	return y.fetchVideos(ctx, opts, videoIDs)
}

func (y *YouTube) resolveChannelID(ctx context.Context) (string, error) {
	target := y.Target
	if strings.HasPrefix(target, "UC") && len(target) == 24 {
		return target, nil
	}

	handle := target
	if !strings.HasPrefix(handle, "@") {
		handle = "@" + handle
	}

	url := fmt.Sprintf("%s/channels?part=id&forHandle=%s&key=%s", y.baseURL(), handle, y.APIKey)
	var resp channelListResponse
	if err := y.get(ctx, url, &resp); err != nil {
		return "", err
	}
	if len(resp.Items) == 0 {
		return "", fmt.Errorf("channel not found for handle %q", y.Target)
	}
	return resp.Items[0].ID, nil
}

func (y *YouTube) fetchChannelStats(ctx context.Context, opts FetchOptions, channelID string) ([]Record, error) {
	url := fmt.Sprintf("%s/channels?part=statistics&id=%s&key=%s", y.baseURL(), channelID, y.APIKey)
	var resp channelListResponse
	if err := y.get(ctx, url, &resp); err != nil {
		return nil, err
	}
	if len(resp.Items) == 0 {
		return nil, fmt.Errorf("channel %s not found", channelID)
	}

	stats := resp.Items[0].Statistics
	date := dateutil.FormatDate(opts.EndDate)

	var records []Record

	if !stats.HiddenSubscriberCount {
		records = append(records, Record{
			Metric:    "total_subscribers",
			ProjectID: opts.ProjectID,
			Target:    y.Target,
			Date:      date,
			Value:     stats.SubscriberCount,
		})
	}
	records = append(records, Record{
		Metric:    "total_channel_views",
		ProjectID: opts.ProjectID,
		Target:    y.Target,
		Date:      date,
		Value:     stats.ViewCount,
	})
	records = append(records, Record{
		Metric:    "total_videos",
		ProjectID: opts.ProjectID,
		Target:    y.Target,
		Date:      date,
		Value:     stats.VideoCount,
	})

	return records, nil
}

func (y *YouTube) fetchPlaylistVideoIDs(ctx context.Context, playlistID string) ([]string, error) {
	var videoIDs []string
	pageToken := ""

	for {
		url := fmt.Sprintf("%s/playlistItems?part=contentDetails&playlistId=%s&maxResults=50&key=%s",
			y.baseURL(), playlistID, y.APIKey)
		if pageToken != "" {
			url += "&pageToken=" + pageToken
		}

		var resp playlistItemsResponse
		if err := y.get(ctx, url, &resp); err != nil {
			return nil, err
		}

		for _, item := range resp.Items {
			videoIDs = append(videoIDs, item.ContentDetails.VideoID)
		}

		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}

	return videoIDs, nil
}

func (y *YouTube) fetchVideos(ctx context.Context, opts FetchOptions, videoIDs []string) ([]Record, error) {
	var records []Record
	date := dateutil.FormatDate(opts.EndDate)

	err := forStringBatches(videoIDs, 50, func(batch []string) error {
		url := fmt.Sprintf("%s/videos?part=statistics,snippet,contentDetails&id=%s&key=%s",
			y.baseURL(), strings.Join(batch, ","), y.APIKey)

		var resp videoListResponse
		if err := y.get(ctx, url, &resp); err != nil {
			return err
		}

		for _, item := range resp.Items {
			records = append(records, youtubeVideoRecords(opts.ProjectID, y.Target, date, item)...)
			y.contentEntries = append(y.contentEntries, youtubeContentEntry(y.Target, item))
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return records, nil
}

func forStringBatches(items []string, size int, fn func([]string) error) error {
	for i := 0; i < len(items); i += size {
		end := i + size
		if end > len(items) {
			end = len(items)
		}
		if err := fn(items[i:end]); err != nil {
			return err
		}
	}
	return nil
}

var youtubeVideoMetrics = []struct {
	name  string
	value func(videoStats) (int64, bool)
}{
	{"total_views", func(s videoStats) (int64, bool) { return s.ViewCount, true }},
	{"total_likes", func(s videoStats) (int64, bool) {
		if s.LikeCount == nil {
			return 0, false
		}
		return *s.LikeCount, true
	}},
	{"total_comments", func(s videoStats) (int64, bool) {
		if s.CommentCount == nil {
			return 0, false
		}
		return *s.CommentCount, true
	}},
}

func youtubeVideoRecords(projectID, target, date string, item videoItem) []Record {
	records := make([]Record, 0, len(youtubeVideoMetrics))
	tags := map[string]string{"video_id": item.ID}
	for _, metric := range youtubeVideoMetrics {
		value, ok := metric.value(item.Statistics)
		if !ok {
			continue
		}
		records = append(records, Record{
			Metric:    metric.name,
			ProjectID: projectID,
			Target:    target,
			Date:      date,
			Value:     value,
			Extra:     copyTags(tags),
		})
	}
	return records
}

func youtubeContentEntry(target string, item videoItem) ContentEntry {
	return ContentEntry{
		Source:      "youtube",
		Target:      target,
		ID:          item.ID,
		Title:       item.Snippet.Title,
		PublishedAt: item.Snippet.PublishedAt,
		URL:         "https://youtube.com/watch?v=" + item.ID,
		Duration:    youtubeDuration(item.ContentDetails.Duration),
		Tags:        item.Snippet.Tags,
		Type:        "video",
	}
}

func youtubeDuration(raw string) *int64 {
	if raw == "" || raw == "P0D" {
		return nil
	}
	duration := parseISO8601Duration(raw)
	if duration == 0 {
		return nil
	}
	return &duration
}

func (y *YouTube) baseURL() string {
	if y.BaseURL != "" {
		return y.BaseURL
	}
	return "https://www.googleapis.com/youtube/v3"
}

func (y *YouTube) get(ctx context.Context, url string, result interface{}) error {
	return doJSONInto(ctx, y.Client, httpJSONRequest{
		URL:          url,
		RequestError: "request",
		StatusError:  "youtube API returned",
	}, result)
}

func detectYouTubeType(target string) string {
	switch {
	case strings.HasPrefix(target, "@"):
		return "channel"
	case strings.HasPrefix(target, "UC") && len(target) == 24:
		return "channel"
	case strings.HasPrefix(target, "PL"):
		return "playlist"
	case len(target) == 11:
		return "video"
	case len(target) > 11:
		return "channel"
	default:
		return ""
	}
}

func parseISO8601Duration(d string) int64 {
	var total int64
	d = strings.TrimPrefix(d, "PT")
	var num string
	for _, c := range d {
		switch c {
		case 'H':
			if n := parseInt(num); n > 0 {
				total += n * 3600
			}
			num = ""
		case 'M':
			if n := parseInt(num); n > 0 {
				total += n * 60
			}
			num = ""
		case 'S':
			if n := parseInt(num); n > 0 {
				total += n
			}
			num = ""
		default:
			num += string(c)
		}
	}
	return total
}

func parseInt(s string) int64 {
	var n int64
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int64(c-'0')
		}
	}
	return n
}

// API response types

type channelListResponse struct {
	Items []channelItem `json:"items"`
}

type channelItem struct {
	ID             string          `json:"id"`
	Statistics     channelStats    `json:"statistics"`
	ContentDetails *channelContent `json:"contentDetails"`
}

type channelStats struct {
	ViewCount             int64 `json:"viewCount,string"`
	SubscriberCount       int64 `json:"subscriberCount,string"`
	VideoCount            int64 `json:"videoCount,string"`
	HiddenSubscriberCount bool  `json:"hiddenSubscriberCount"`
}

type channelContent struct {
	RelatedPlaylists struct {
		Uploads string `json:"uploads"`
	} `json:"relatedPlaylists"`
}

type playlistItemsResponse struct {
	Items         []playlistItem `json:"items"`
	NextPageToken string         `json:"nextPageToken"`
}

type playlistItem struct {
	ContentDetails struct {
		VideoID string `json:"videoId"`
	} `json:"contentDetails"`
}

type videoListResponse struct {
	Items []videoItem `json:"items"`
}

type videoItem struct {
	ID             string              `json:"id"`
	Snippet        videoSnippet        `json:"snippet"`
	ContentDetails videoContentDetails `json:"contentDetails"`
	Statistics     videoStats          `json:"statistics"`
}

type videoSnippet struct {
	Title       string   `json:"title"`
	PublishedAt string   `json:"publishedAt"`
	Tags        []string `json:"tags"`
}

type videoContentDetails struct {
	Duration string `json:"duration"`
}

type videoStats struct {
	ViewCount    int64  `json:"viewCount,string"`
	LikeCount    *int64 `json:"likeCount,string"`
	CommentCount *int64 `json:"commentCount,string"`
}
