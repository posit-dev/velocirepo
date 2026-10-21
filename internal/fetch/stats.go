package fetch

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"
)

type Stats struct {
	mu           sync.Mutex
	StartTime    time.Time
	APICalls     map[string]int // host -> count
	TotalJobs    int
	Succeeded    int
	Skipped      int
	Failed       int
	Records      int
	FilesWritten int

	quota        map[string]Quota
	deprecations map[string]bool
}

// Quota is the rate limit left for an API, from its response headers.
type Quota struct {
	Limit     int   `json:"limit"`
	Remaining int   `json:"remaining"`
	Reset     int64 `json:"reset,omitempty"`
}

func NewStats(totalJobs int) *Stats {
	return &Stats{
		StartTime: time.Now(),
		APICalls:  make(map[string]int),
		TotalJobs: totalJobs,
	}
}

func (s *Stats) AddAPICall(host string) {
	s.mu.Lock()
	s.APICalls[host]++
	s.mu.Unlock()
}

func (s *Stats) AddResult(r Result) {
	s.mu.Lock()
	switch {
	case r.Error != "":
		s.Failed++
	case r.Skipped != "":
		s.Skipped++
	default:
		s.Succeeded++
		s.Records += r.Records
		s.FilesWritten += r.Files
	}
	s.mu.Unlock()
}

func (s *Stats) Completed() int {
	s.mu.Lock()
	n := s.Succeeded + s.Skipped + s.Failed
	s.mu.Unlock()
	return n
}

func (s *Stats) Elapsed() time.Duration {
	return time.Since(s.StartTime)
}

type countingTransport struct {
	base  http.RoundTripper
	stats *Stats
}

func (t *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Host
	if host == "" {
		if u, err := url.Parse(req.URL.String()); err == nil {
			host = u.Host
		}
	}
	t.stats.AddAPICall(host)
	resp, err := t.base.RoundTrip(req)
	if err == nil && resp != nil {
		t.stats.observe(host, req.URL.Path, resp.Header)
	}
	return resp, err
}

// observe records the quota left and any deprecation notice in a response's
// headers.
func (s *Stats) observe(host, path string, h http.Header) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit, err := strconv.Atoi(h.Get("X-RateLimit-Limit")); err == nil {
		remaining, _ := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
		reset, _ := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64)
		key := host
		if resource := h.Get("X-RateLimit-Resource"); resource != "" {
			key += "/" + resource
		}
		if s.quota == nil {
			s.quota = make(map[string]Quota)
		}
		// Keep the lowest remaining quota seen, which is the latest.
		if prev, ok := s.quota[key]; !ok || remaining < prev.Remaining {
			s.quota[key] = Quota{Limit: limit, Remaining: remaining, Reset: reset}
		}
	}
	dep, sunset := h.Get("Deprecation"), h.Get("Sunset")
	if dep == "" && sunset == "" {
		return
	}
	if s.deprecations == nil {
		s.deprecations = make(map[string]bool)
	}
	if len(s.deprecations) < 100 {
		s.deprecations[fmt.Sprintf("%s%s: deprecation=%q sunset=%q", host, path, dep, sunset)] = true
	}
}

// QuotaSnapshot returns the quota left per API.
func (s *Stats) QuotaSnapshot() map[string]Quota {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Quota, len(s.quota))
	for k, v := range s.quota {
		out[k] = v
	}
	return out
}

// DeprecationsSnapshot returns the endpoints that announced their removal,
// sorted.
func (s *Stats) DeprecationsSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.deprecations))
	for d := range s.deprecations {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func CountingClient(stats *Stats) *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &countingTransport{
			base:  http.DefaultTransport,
			stats: stats,
		},
	}
}
