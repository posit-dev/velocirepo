package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type readJSONLOptions struct {
	skipInvalid bool
	wrapErrors  bool
}

func writeJSONLAtomic[T any](path string, items []T, label string) error {
	return writeJSONLWith(path, func(w *bufio.Writer) error {
		for _, item := range items {
			data, err := json.Marshal(item)
			if err != nil {
				return fmt.Errorf("marshal %s: %w", label, err)
			}
			_, _ = w.Write(data)
			_ = w.WriteByte('\n')
		}
		return nil
	})
}

func writeJSONLLinesAtomic(path string, lines [][]byte) error {
	return writeJSONLWith(path, func(w *bufio.Writer) error {
		for _, line := range lines {
			_, _ = w.Write(line)
			_ = w.WriteByte('\n')
		}
		return nil
	})
}

func writeJSONLWith(path string, write func(*bufio.Writer) error) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*.jsonl")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()

	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	w := bufio.NewWriter(tmp)
	if err := write(w); err != nil {
		cleanup()
		return err
	}
	if err := w.Flush(); err != nil {
		cleanup()
		return fmt.Errorf("flush %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, path, err)
	}
	return nil
}

func readJSONL[T any](path string, opts readJSONLOptions) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		if opts.wrapErrors {
			return nil, fmt.Errorf("open %s: %w", path, err)
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var items []T
	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimRight(line, "\n")
			if len(line) == 0 {
				if err != nil {
					break
				}
				continue
			}
			var item T
			if jsonErr := json.Unmarshal(line, &item); jsonErr != nil {
				if opts.skipInvalid {
					if err != nil {
						break
					}
					continue
				}
				if opts.wrapErrors {
					return nil, fmt.Errorf("unmarshal line in %s: %w", path, jsonErr)
				}
				return nil, jsonErr
			}
			items = append(items, item)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			if opts.wrapErrors {
				return nil, fmt.Errorf("read %s: %w", path, err)
			}
			return nil, err
		}
	}
	return items, nil
}

func groupBy[T any](items []T, key func(T) string) map[string][]T {
	grouped := make(map[string][]T)
	for _, item := range items {
		groupKey := key(item)
		grouped[groupKey] = append(grouped[groupKey], item)
	}
	return grouped
}
