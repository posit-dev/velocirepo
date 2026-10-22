package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/posit-dev/velocirepo/internal/source"
)

// keptHeaders are the response headers worth archiving: rate limits,
// pagination, and notices of APIs that are going away.
var keptHeaders = []string{"Link", "Deprecation", "Sunset", "Warning",
	"X-Ratelimit-Limit", "X-Ratelimit-Remaining", "X-Ratelimit-Reset", "X-Ratelimit-Resource"}

// secretParams are query parameters and request body fields that carry
// credentials.
var secretParams = map[string]bool{"key": true, "token": true, "access_token": true, "api_key": true, "client_secret": true}

// Capture is an http.RoundTripper that archives the responses of requests
// whose data can't be fetched again. Which requests those are, the source
// declares (source.Meta.Archive), or marks per request
// (source.WithArchiveMode). Request headers, with their credentials, are never
// archived.
type Capture struct {
	Base   http.RoundTripper
	Writer *Writer
	// Now returns the time of an exchange; nil means time.Now.
	Now func() time.Time
}

func (c *Capture) RoundTrip(req *http.Request) (*http.Response, error) {
	info, _ := source.RequestInfoOf(req.Context())
	mode := archiveMode(req, info)
	if mode != source.ArchiveFull && mode != source.ArchiveReadFields {
		return c.Base.RoundTrip(req)
	}

	var reqBody []byte
	if req.Body != nil {
		var err error
		reqBody, err = io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}

	resp, err := c.Base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))

	archived := body
	if mode == source.ArchiveReadFields {
		meta, _ := source.MetaFor(info.Source)
		if meta.Reduce == nil {
			return resp, nil
		}
		reduced, ok := meta.Reduce(req.URL.String(), body)
		if !ok {
			return resp, nil
		}
		archived = reduced
	}

	redactedBody := redactBody(reqBody)
	ex := Exchange{
		Time:        c.now().UTC(),
		Source:      info.Source,
		Project:     info.Project,
		Target:      info.Target,
		Method:      req.Method,
		URL:         RedactURL(req.URL),
		RequestBody: string(redactedBody),
		BodyHash:    bodyHash(redactedBody),
		Status:      resp.StatusCode,
		Headers:     keptResponseHeaders(resp.Header),
		Body:        string(archived),
	}
	if err := c.Writer.Write(ex); err != nil {
		slog.Warn("archive response", "source", info.Source, "error", err)
	}
	return resp, nil
}

func (c *Capture) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func archiveMode(req *http.Request, info source.RequestInfo) source.ArchiveMode {
	if mode, ok := source.ArchiveModeOf(req.Context()); ok {
		return mode
	}
	if info.Source == "" {
		return source.ArchiveUndeclared
	}
	meta, ok := source.MetaFor(info.Source)
	if !ok {
		return source.ArchiveUndeclared
	}
	return meta.Archive
}

func keptResponseHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for _, name := range keptHeaders {
		if v := h.Get(name); v != "" {
			out[name] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// RedactURL returns a URL with the values of secret query parameters
// replaced.
func RedactURL(u *url.URL) string {
	redacted := *u
	q := redacted.Query()
	changed := false
	for name := range q {
		if secretParams[strings.ToLower(name)] {
			q.Set(name, "REDACTED")
			changed = true
		}
	}
	if changed {
		redacted.RawQuery = q.Encode()
	}
	redacted.User = nil
	return redacted.String()
}

// redactBody replaces the values of secret fields in a JSON request body.
// Other bodies are returned as they are.
func redactBody(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return body
	}
	if !redactValue(v) {
		return body
	}
	out, err := json.Marshal(v)
	if err != nil {
		return body
	}
	return out
}

func redactValue(v any) bool {
	changed := false
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if secretParams[strings.ToLower(k)] {
				x[k] = "REDACTED"
				changed = true
				continue
			}
			if redactValue(val) {
				changed = true
			}
		}
	case []any:
		for _, val := range x {
			if redactValue(val) {
				changed = true
			}
		}
	}
	return changed
}

func bodyHash(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
