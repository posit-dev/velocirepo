package sourceinfo

// Category is the date-partitioned data category for a source.
type Category string

const (
	CategoryEvents  Category = "events"
	CategoryMetrics Category = "metrics"
)

// Descriptor captures source metadata shared by config, CLI, MCP, fetch, and
// store path handling.
type Descriptor struct {
	Name                 string
	DisplayName          string
	Category             Category
	ContentDir           string
	ConfigField          string
	TOMLKey              string
	CLIFlag              string
	CLIUsage             string
	AddPrompt            string
	UpdatePrompt         string
	JSONKeys             []string
	CSVColumns           []string
	MCPKey               string
	MCPAddDescription    string
	MCPUpdateDescription string
	FetchToolName        string
	FetchDescription     string
	TokenEnv             string
}

var descriptors = []Descriptor{
	{
		Name:                 "github",
		DisplayName:          "GitHub",
		Category:             CategoryEvents,
		ConfigField:          "GitHubEvents",
		TOMLKey:              "github",
		CLIFlag:              "github",
		CLIUsage:             "GitHub owner/repo",
		AddPrompt:            "GitHub (owner/repo)",
		UpdatePrompt:         "GitHub (owner/repo)",
		JSONKeys:             []string{"github"},
		CSVColumns:           []string{"github"},
		MCPKey:               "github",
		MCPAddDescription:    "GitHub owner/repo for events",
		MCPUpdateDescription: "GitHub owner/repo for events (empty to remove)",
		FetchToolName:        "fetch_github",
		FetchDescription:     "Fetch GitHub events (stars, forks, issues, PRs).",
	},
	{
		Name:                 "github-traffic",
		DisplayName:          "GitHub Traffic",
		Category:             CategoryMetrics,
		ConfigField:          "GitHubTraffic",
		TOMLKey:              "github-traffic",
		CLIFlag:              "github-traffic",
		CLIUsage:             "GitHub owner/repo for traffic data",
		AddPrompt:            "GitHub traffic (owner/repo)",
		UpdatePrompt:         "GitHub traffic (owner/repo)",
		JSONKeys:             []string{"github_traffic", "github-traffic"},
		CSVColumns:           []string{"github-traffic", "github_traffic"},
		MCPKey:               "github_traffic",
		MCPAddDescription:    "GitHub owner/repo for traffic",
		MCPUpdateDescription: "GitHub owner/repo for traffic (empty to remove)",
		FetchToolName:        "fetch_traffic",
		FetchDescription:     "Fetch GitHub traffic data (views and clones). Requires GITHUB_TOKEN with admin access.",
		TokenEnv:             "GITHUB_TOKEN",
	},
	{
		Name:                 "pypi",
		DisplayName:          "PyPI",
		Category:             CategoryMetrics,
		ConfigField:          "PyPI",
		TOMLKey:              "pypi",
		CLIFlag:              "pypi",
		CLIUsage:             "PyPI package name",
		AddPrompt:            "PyPI package",
		UpdatePrompt:         "PyPI package",
		JSONKeys:             []string{"pypi"},
		CSVColumns:           []string{"pypi"},
		MCPKey:               "pypi",
		MCPAddDescription:    "PyPI package name",
		MCPUpdateDescription: "PyPI package name (empty to remove)",
		FetchToolName:        "fetch_pypi",
		FetchDescription:     "Fetch PyPI download statistics.",
	},
	{
		Name:                 "cran",
		DisplayName:          "CRAN",
		Category:             CategoryMetrics,
		ConfigField:          "CRAN",
		TOMLKey:              "cran",
		CLIFlag:              "cran",
		CLIUsage:             "CRAN package name",
		AddPrompt:            "CRAN package",
		UpdatePrompt:         "CRAN package",
		JSONKeys:             []string{"cran"},
		CSVColumns:           []string{"cran"},
		MCPKey:               "cran",
		MCPAddDescription:    "CRAN package name",
		MCPUpdateDescription: "CRAN package name (empty to remove)",
		FetchToolName:        "fetch_cran",
		FetchDescription:     "Fetch CRAN download statistics.",
	},
	{
		Name:                 "homebrew",
		DisplayName:          "Homebrew",
		Category:             CategoryMetrics,
		ConfigField:          "Homebrew",
		TOMLKey:              "homebrew",
		CLIFlag:              "homebrew",
		CLIUsage:             "Homebrew formula",
		AddPrompt:            "Homebrew formula",
		UpdatePrompt:         "Homebrew formula",
		JSONKeys:             []string{"homebrew"},
		CSVColumns:           []string{"homebrew"},
		MCPKey:               "homebrew",
		MCPAddDescription:    "Homebrew formula",
		MCPUpdateDescription: "Homebrew formula (empty to remove)",
		FetchToolName:        "fetch_homebrew",
		FetchDescription:     "Fetch Homebrew install counts.",
	},
	{
		Name:                 "plausible",
		DisplayName:          "Plausible",
		Category:             CategoryMetrics,
		ConfigField:          "Plausible",
		TOMLKey:              "plausible",
		CLIFlag:              "plausible",
		CLIUsage:             "Plausible site ID",
		AddPrompt:            "Plausible site ID",
		UpdatePrompt:         "Plausible site ID",
		JSONKeys:             []string{"plausible"},
		CSVColumns:           []string{"plausible"},
		MCPKey:               "plausible",
		MCPAddDescription:    "Plausible site ID",
		MCPUpdateDescription: "Plausible site ID (empty to remove)",
		FetchToolName:        "fetch_plausible",
		FetchDescription:     "Fetch Plausible analytics (pageviews, visitors, visits). Requires PLAUSIBLE_TOKEN.",
		TokenEnv:             "PLAUSIBLE_TOKEN",
	},
	{
		Name:                 "openvsx",
		DisplayName:          "OpenVSX",
		Category:             CategoryMetrics,
		ConfigField:          "OpenVSX",
		TOMLKey:              "openvsx",
		CLIFlag:              "openvsx",
		CLIUsage:             "OpenVSX extension (publisher/extension)",
		AddPrompt:            "OpenVSX extension",
		UpdatePrompt:         "OpenVSX extension",
		JSONKeys:             []string{"openvsx"},
		CSVColumns:           []string{"openvsx"},
		MCPKey:               "openvsx",
		MCPAddDescription:    "OpenVSX extension (publisher/extension)",
		MCPUpdateDescription: "OpenVSX extension (empty to remove)",
		FetchToolName:        "fetch_openvsx",
		FetchDescription:     "Fetch Open VSX extension metrics (downloads, reviews, ratings).",
	},
	{
		Name:                 "youtube",
		DisplayName:          "YouTube",
		Category:             CategoryMetrics,
		ContentDir:           "content/youtube",
		ConfigField:          "YouTube",
		TOMLKey:              "youtube",
		CLIFlag:              "youtube",
		CLIUsage:             "YouTube channel (@handle), playlist (PLxxx), or video ID",
		AddPrompt:            "YouTube (@handle, PLxxx, or video ID)",
		UpdatePrompt:         "YouTube (@handle, PLxxx, or video ID)",
		JSONKeys:             []string{"youtube"},
		CSVColumns:           []string{"youtube"},
		MCPKey:               "youtube",
		MCPAddDescription:    "YouTube channel (@handle), playlist, or video ID",
		MCPUpdateDescription: "YouTube target (empty to remove)",
		FetchToolName:        "fetch_youtube",
		FetchDescription:     "Fetch YouTube metrics (views, likes, comments, subscribers) and video content index. Requires YOUTUBE_TOKEN.",
		TokenEnv:             "YOUTUBE_TOKEN",
	},
	{
		Name:                 "linkedin",
		DisplayName:          "LinkedIn",
		Category:             CategoryMetrics,
		ContentDir:           "content/linkedin",
		ConfigField:          "LinkedIn",
		TOMLKey:              "linkedin",
		CLIFlag:              "linkedin",
		CLIUsage:             "LinkedIn URN",
		AddPrompt:            "LinkedIn URN",
		UpdatePrompt:         "LinkedIn URN",
		JSONKeys:             []string{"linkedin"},
		CSVColumns:           []string{"linkedin"},
		MCPKey:               "linkedin",
		MCPAddDescription:    "LinkedIn URN (urn:li:organization:ID)",
		MCPUpdateDescription: "LinkedIn URN (empty to remove)",
		FetchToolName:        "fetch_linkedin",
		FetchDescription:     "Fetch LinkedIn post metrics (impressions, likes, comments, shares) and content index. Requires LINKEDIN_TOKEN.",
		TokenEnv:             "LINKEDIN_TOKEN",
	},
	{
		Name:                 "rss",
		DisplayName:          "RSS/Atom",
		Category:             CategoryMetrics,
		ContentDir:           "content/rss",
		ConfigField:          "RSS",
		TOMLKey:              "rss",
		CLIFlag:              "rss",
		CLIUsage:             "RSS or Atom feed URL",
		AddPrompt:            "RSS/Atom feed URL",
		UpdatePrompt:         "RSS/Atom feed URL",
		JSONKeys:             []string{"rss"},
		CSVColumns:           []string{"rss"},
		MCPKey:               "rss",
		MCPAddDescription:    "RSS or Atom feed URL",
		MCPUpdateDescription: "RSS or Atom feed URL (empty to remove)",
		FetchToolName:        "fetch_rss",
		FetchDescription:     "Fetch items from an RSS or Atom feed as content entries, plus a total_items metric.",
	},
}

var byName = func() map[string]Descriptor {
	m := make(map[string]Descriptor, len(descriptors))
	for _, d := range descriptors {
		m[d.Name] = cloneDescriptor(d)
	}
	return m
}()

var byMCPKey = func() map[string]Descriptor {
	m := make(map[string]Descriptor, len(descriptors))
	for _, d := range descriptors {
		m[d.MCPKey] = cloneDescriptor(d)
	}
	return m
}()

func All() []Descriptor {
	out := make([]Descriptor, len(descriptors))
	for i, d := range descriptors {
		out[i] = cloneDescriptor(d)
	}
	return out
}

func Get(name string) (Descriptor, bool) {
	d, ok := byName[name]
	return cloneDescriptor(d), ok
}

func Must(name string) Descriptor {
	d, ok := Get(name)
	if !ok {
		panic("unknown source: " + name)
	}
	return d
}

func GetByMCPKey(key string) (Descriptor, bool) {
	d, ok := byMCPKey[key]
	return cloneDescriptor(d), ok
}

func IsEvent(name string) bool {
	d, ok := Get(name)
	return ok && d.Category == CategoryEvents
}

func CategoryDir(name string) string {
	if d, ok := Get(name); ok {
		return string(d.Category)
	}
	return string(CategoryMetrics)
}

func DataDirPath(name string) string {
	return CategoryDir(name) + "/" + name
}

func DataDirPaths() []string {
	paths := make([]string, 0, len(descriptors))
	for _, d := range descriptors {
		paths = append(paths, DataDirPath(d.Name))
		if d.ContentDir != "" {
			paths = append(paths, d.ContentDir)
		}
	}
	return paths
}

func EventNames() []string {
	var names []string
	for _, d := range descriptors {
		if d.Category == CategoryEvents {
			names = append(names, d.Name)
		}
	}
	return names
}

func MetricNames() []string {
	var names []string
	for _, d := range descriptors {
		if d.Category == CategoryMetrics {
			names = append(names, d.Name)
		}
	}
	return names
}

func cloneDescriptor(d Descriptor) Descriptor {
	d.JSONKeys = append([]string(nil), d.JSONKeys...)
	d.CSVColumns = append([]string(nil), d.CSVColumns...)
	return d
}
