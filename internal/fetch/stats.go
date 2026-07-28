package fetch

import (
	"net/http"
	"net/url"
	"sync"
	"time"
)

type Stats struct {
	mu          sync.Mutex
	StartTime   time.Time
	APICalls    map[string]int // host -> count
	TotalJobs   int
	Succeeded   int
	Skipped     int
	Failed      int
	Records     int
	FilesWritten int
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
	return t.base.RoundTrip(req)
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
