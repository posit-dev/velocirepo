// Package archive keeps the raw API responses of data that can't be fetched
// again, so a processing bug can be repaired by replaying them.
package archive

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Exchange is one archived HTTP request and its response.
type Exchange struct {
	Time    time.Time `json:"time"`
	Source  string    `json:"source"`
	Project string    `json:"project"`
	Target  string    `json:"target"`
	Method  string    `json:"method"`
	// URL is the request URL with secrets redacted.
	URL string `json:"url"`
	// RequestBody is the redacted request body; BodyHash is its SHA-256,
	// which replay uses to match requests.
	RequestBody string            `json:"request_body,omitempty"`
	BodyHash    string            `json:"body_hash,omitempty"`
	Status      int               `json:"status"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        string            `json:"body"`
}

// Job records the options a fetch job ran with, so replay makes exactly the
// same requests later, whatever has been stored since.
type Job struct {
	Source  string `json:"source"`
	Project string `json:"project"`
	Target  string `json:"target"`
	// Start and End are the job's dates (YYYY-MM-DD), per series; "" is the
	// source's main series.
	Start map[string]string `json:"start"`
	End   map[string]string `json:"end"`
	// Zones are the resolved time zones per series.
	Zones map[string]string `json:"zones,omitempty"`
	// SnapshotRefs are the issue and pull request numbers GitHub's reaction
	// counts were requested for.
	SnapshotRefs []int `json:"snapshot_refs,omitempty"`
}

// File describes one archive file of a run.
type File struct {
	Name      string `json:"name"`
	Source    string `json:"source"`
	SHA256    string `json:"sha256"`
	Exchanges int    `json:"exchanges"`
}

// Manifest describes an archived run.
type Manifest struct {
	RunID      string    `json:"run_id"`
	Version    string    `json:"version"`
	ConfigHash string    `json:"config_hash"`
	RunTime    time.Time `json:"run_time"`
	Jobs       []Job     `json:"jobs"`
	Files      []File    `json:"files"`
}

// NewRunID returns an ID for a run at the given time. In GitHub Actions it
// includes the run ID and attempt, so a re-run doesn't collide with the
// first attempt; elsewhere a random suffix.
func NewRunID(at time.Time, getenv func(string) string) string {
	prefix := at.UTC().Format("20060102T150405Z")
	if id := getenv("GITHUB_RUN_ID"); id != "" {
		attempt := getenv("GITHUB_RUN_ATTEMPT")
		if attempt == "" {
			attempt = "1"
		}
		return prefix + "-" + id + "-" + attempt
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	return prefix + "-local-" + hex.EncodeToString(b[:])
}

// AssetName is the release asset name of a run's archive file for a source.
func AssetName(runID, sourceName string) string {
	return runID + "-" + sourceName + ".jsonl.zst"
}

// ManifestAssetName is the release asset name of a run's manifest.
func ManifestAssetName(runID string) string {
	return runID + "-manifest.json"
}

// ReleaseTag is the tag of the release that holds a month's archives.
func ReleaseTag(at time.Time) string {
	return "raw-" + at.UTC().Format("2006-01")
}
