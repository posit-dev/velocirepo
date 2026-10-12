package store

import (
	"encoding/json"
	"os"

	"github.com/posit-dev/velocirepo/internal/source"
)

func readYouTubeIndex(path string) ([]source.YouTubeIndexEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var entries []source.YouTubeIndexEntry
	scanner := newLineReader(f)
	for scanner.Scan() {
		var e source.YouTubeIndexEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	return entries, scanner.Err()
}
