package archive

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Replay is an http.RoundTripper that serves archived responses instead of
// the network. Requests are matched by method, redacted URL and the hash of
// the redacted body; the same request made several times gets its archived
// responses in order. A request that isn't in the archive is an error, never
// a network call.
type Replay struct {
	mu      sync.Mutex
	pending map[string][]Exchange
}

// NewReplay returns a transport that serves the given exchanges.
func NewReplay(exchanges []Exchange) *Replay {
	r := &Replay{pending: map[string][]Exchange{}}
	for _, e := range exchanges {
		key := replayKey(e.Method, e.URL, e.BodyHash)
		r.pending[key] = append(r.pending[key], e)
	}
	return r
}

func replayKey(method, url, hash string) string {
	return method + " " + url + " " + hash
}

func (r *Replay) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
	}
	url := RedactURL(req.URL)
	key := replayKey(req.Method, url, bodyHash(redactBody(body)))

	r.mu.Lock()
	queue := r.pending[key]
	if len(queue) == 0 {
		r.mu.Unlock()
		return nil, fmt.Errorf("replay: %s %s isn't in the archive", req.Method, url)
	}
	e := queue[0]
	r.pending[key] = queue[1:]
	r.mu.Unlock()

	header := http.Header{}
	for k, v := range e.Headers {
		header.Set(k, v)
	}
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", e.Status, http.StatusText(e.Status)),
		StatusCode:    e.Status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader([]byte(e.Body))),
		ContentLength: int64(len(e.Body)),
		Request:       req,
	}, nil
}

// Run is an archived run: its manifest and its exchanges.
type Run struct {
	Manifest  Manifest
	Exchanges []Exchange
}

// ReadRun reads an archived run from its directory.
func ReadRun(runDir string) (Run, error) {
	m, err := ReadManifest(runDir)
	if err != nil {
		return Run{}, err
	}
	run := Run{Manifest: m}
	for _, f := range m.Files {
		exchanges, err := ReadExchanges(filepath.Join(runDir, f.Name))
		if err != nil {
			return Run{}, fmt.Errorf("read %s: %w", f.Name, err)
		}
		run.Exchanges = append(run.Exchanges, exchanges...)
	}
	return run, nil
}

// RunsInRange returns the runs in dir that have a job of the source and
// project ("" for any) whose end date falls from one day through another,
// oldest first, for a repair of that range.
func RunsInRange(dir, sourceName, project, from, to string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type found struct {
		dir string
		at  time.Time
	}
	var runs []found
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		runDir := filepath.Join(dir, e.Name())
		m, err := ReadManifest(runDir)
		if err != nil {
			continue
		}
		for _, j := range m.Jobs {
			if (sourceName != "" && j.Source != sourceName) || (project != "" && j.Project != project) {
				continue
			}
			if end := j.End[""]; end >= from && end <= to {
				runs = append(runs, found{runDir, m.RunTime})
				break
			}
		}
	}
	sort.SliceStable(runs, func(i, k int) bool { return runs[i].at.Before(runs[k].at) })
	out := make([]string, len(runs))
	for i, r := range runs {
		out[i] = r.dir
	}
	return out, nil
}
