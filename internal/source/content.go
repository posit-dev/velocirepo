package source

type ContentEntry struct {
	Source      string         `json:"source"`
	ProjectID   string         `json:"project_id"`
	Target      string         `json:"target"`
	ID          string         `json:"id"`
	Ref         *int           `json:"ref,omitempty"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Content     string         `json:"content,omitempty"`
	PublishedAt string         `json:"published_at"`
	UpdatedAt   string         `json:"updated_at,omitempty"`
	URL         string         `json:"url,omitempty"`
	Duration    *int64         `json:"duration,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Type        string         `json:"type,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
}

type ContentProvider interface {
	ContentEntries() []ContentEntry
	ContentFilename() string
}

type MultiContentProvider interface {
	ContentByFilename() map[string][]ContentEntry
}
