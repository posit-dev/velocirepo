package source

type ContentEntry struct {
	Source      string         `json:"source"`
	ProjectID   string         `json:"project_id"`
	Target      string         `json:"target"`
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Content     string         `json:"content,omitempty"`
	PublishedAt string         `json:"published_at"`
	UpdatedAt   string         `json:"updated_at,omitempty"`
	URL         string         `json:"url,omitempty"`
	Duration    *int64         `json:"duration,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Type        string         `json:"type,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type ContentProvider interface {
	ContentEntries() []ContentEntry
	ContentFilename() string
}
