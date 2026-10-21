package archive

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Writer writes a run's archive: one zstd-compressed JSONL file per source,
// and a manifest.
type Writer struct {
	dir      string
	manifest Manifest

	mu    sync.Mutex
	files map[string]*sourceFile
	jobs  map[string]int // job key -> index in manifest.Jobs
}

type sourceFile struct {
	name  string
	file  *os.File
	enc   *zstd.Encoder
	count int
}

// NewWriter creates <dir>/<runID>/ for a run's archive.
func NewWriter(dir, runID, version string, runTime time.Time) (*Writer, error) {
	runDir := filepath.Join(dir, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, fmt.Errorf("create archive directory: %w", err)
	}
	return &Writer{
		dir:      runDir,
		manifest: Manifest{RunID: runID, Version: version, RunTime: runTime.UTC()},
		files:    make(map[string]*sourceFile),
		jobs:     make(map[string]int),
	}, nil
}

// Dir is the run's archive directory.
func (w *Writer) Dir() string { return w.dir }

// Write appends an exchange to its source's file.
func (w *Writer) Write(e Exchange) error {
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode exchange: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	f, ok := w.files[e.Source]
	if !ok {
		name := e.Source + ".jsonl.zst"
		file, err := os.Create(filepath.Join(w.dir, name))
		if err != nil {
			return fmt.Errorf("create archive file: %w", err)
		}
		enc, err := zstd.NewWriter(file)
		if err != nil {
			_ = file.Close()
			return err
		}
		f = &sourceFile{name: name, file: file, enc: enc}
		w.files[e.Source] = f
	}
	if _, err := f.enc.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}
	f.count++
	return nil
}

func jobKey(source, project, target string) string {
	return source + "\x00" + project + "\x00" + target
}

// AddJob records a job's options in the manifest.
func (w *Writer) AddJob(j Job) {
	w.mu.Lock()
	defer w.mu.Unlock()
	key := jobKey(j.Source, j.Project, j.Target)
	if i, ok := w.jobs[key]; ok {
		refs := w.manifest.Jobs[i].SnapshotRefs
		w.manifest.Jobs[i] = j
		if len(j.SnapshotRefs) == 0 {
			w.manifest.Jobs[i].SnapshotRefs = refs
		}
		return
	}
	w.jobs[key] = len(w.manifest.Jobs)
	w.manifest.Jobs = append(w.manifest.Jobs, j)
}

// SetSnapshotRefs records the issue and pull request numbers a job's
// snapshots were requested for.
func (w *Writer) SetSnapshotRefs(source, project, target string, refs []int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if i, ok := w.jobs[jobKey(source, project, target)]; ok {
		w.manifest.Jobs[i].SnapshotRefs = append([]int(nil), refs...)
	}
}

// Close finishes the archive files and writes the manifest.
func (w *Writer) Close() (Manifest, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var errs []error
	sources := make([]string, 0, len(w.files))
	for s := range w.files {
		sources = append(sources, s)
	}
	sort.Strings(sources)
	w.manifest.Files = nil
	for _, s := range sources {
		f := w.files[s]
		if err := f.enc.Close(); err != nil {
			errs = append(errs, err)
		}
		if err := f.file.Close(); err != nil {
			errs = append(errs, err)
		}
		sum, err := fileSHA256(filepath.Join(w.dir, f.name))
		if err != nil {
			errs = append(errs, err)
		}
		w.manifest.Files = append(w.manifest.Files, File{Name: f.name, Source: s, SHA256: sum, Exchanges: f.count})
	}
	w.files = map[string]*sourceFile{}
	data, err := json.MarshalIndent(w.manifest, "", "  ")
	if err != nil {
		errs = append(errs, err)
	} else if err := os.WriteFile(filepath.Join(w.dir, "manifest.json"), append(data, '\n'), 0o644); err != nil {
		errs = append(errs, err)
	}
	return w.manifest, errors.Join(errs...)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ReadExchanges reads an archive file.
func ReadExchanges(path string) ([]Exchange, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	dec, err := zstd.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer dec.Close()

	var out []Exchange
	reader := bufio.NewReader(dec)
	for {
		line, err := reader.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var e Exchange
			if jerr := json.Unmarshal(line, &e); jerr != nil {
				return nil, fmt.Errorf("parse %s: %w", path, jerr)
			}
			out = append(out, e)
		}
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// ReadManifest reads a run's manifest.
func ReadManifest(runDir string) (Manifest, error) {
	var m Manifest
	data, err := os.ReadFile(filepath.Join(runDir, "manifest.json"))
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(data, &m)
	return m, err
}

// FindSecrets returns the archive files of a run that contain any of the
// given secrets. Empty and very short values are ignored.
func FindSecrets(runDir string, secrets []string) ([]string, error) {
	entries, err := os.ReadDir(runDir)
	if err != nil {
		return nil, err
	}
	var found []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl.zst") {
			continue
		}
		exchanges, err := ReadExchanges(filepath.Join(runDir, e.Name()))
		if err != nil {
			return nil, err
		}
		if containsAny(exchanges, secrets) {
			found = append(found, e.Name())
		}
	}
	return found, nil
}

func containsAny(exchanges []Exchange, secrets []string) bool {
	for _, ex := range exchanges {
		text := ex.URL + "\n" + ex.RequestBody + "\n" + ex.Body
		for _, v := range ex.Headers {
			text += "\n" + v
		}
		for _, s := range secrets {
			if len(s) >= 8 && strings.Contains(text, s) {
				return true
			}
		}
	}
	return false
}
