package mcp

import (
	"context"

	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/sourceinfo"
	"github.com/posit-dev/velocirepo/internal/version"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type ServerOptions struct {
	Config   *config.Config
	ReadOnly bool
}

func NewServer(opts ServerOptions) *server.MCPServer {
	s := server.NewMCPServer(
		"velocirepo",
		version.Version,
	)

	h := &handlers{cfg: opts.Config}

	s.AddTool(queryTool(), h.handleQuery)
	s.AddTool(schemaTool(), h.handleSchema)
	s.AddTool(listProjectsTool(), h.handleListProjects)
	s.AddTool(showProjectTool(), h.handleShowProject)
	s.AddTool(badgeTool(), h.handleBadge)
	s.AddTool(listViewsTool(), h.handleListViews)
	s.AddTool(showViewTool(), h.handleShowView)
	s.AddTool(versionTool(), h.handleVersion)

	if !opts.ReadOnly {
		s.AddTool(fetchTool(), h.handleFetch)
		for _, desc := range sourceinfo.All() {
			desc := desc
			s.AddTool(fetchSourceTool(desc), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return h.handleFetchSource(ctx, req, desc.Name)
			})
		}
		s.AddTool(addProjectTool(), h.handleAddProject)
		s.AddTool(updateProjectTool(), h.handleUpdateProject)
		s.AddTool(removeProjectTool(), h.handleRemoveProject)
		s.AddTool(renameProjectTool(), h.handleRenameProject)
		s.AddTool(importProjectsTool(), h.handleImportProjects)
		s.AddTool(validateProjectsTool(), h.handleValidateProjects)
		s.AddTool(exportTool(), h.handleExport)
		s.AddTool(migrateTool(), h.handleMigrate)
		s.AddTool(addViewTool(), h.handleAddView)
		s.AddTool(removeViewTool(), h.handleRemoveView)
		s.AddTool(renderViewTool(), h.handleRenderView)
		s.AddTool(renderViewsTool(), h.handleRenderViews)
	}

	return s
}

func queryTool() mcp.Tool {
	return mcp.NewTool("query",
		mcp.WithDescription(`Run a SQL query against the metrics data (DuckDB). Default LIMIT is 1000.

Schema:
  metrics: project VARCHAR, source VARCHAR, target VARCHAR, metric VARCHAR, date DATE, value BIGINT, extra JSON
  events: project VARCHAR, source VARCHAR, type VARCHAR, target VARCHAR, datetime TIMESTAMP, ref INTEGER, user VARCHAR, extra JSON
  content: project VARCHAR, source VARCHAR, target VARCHAR, id VARCHAR, ref INTEGER, title VARCHAR, description VARCHAR, content VARCHAR, published_at TIMESTAMP, updated_at TIMESTAMP, url VARCHAR, duration BIGINT, tags JSON, type VARCHAR, extra JSON
  projects: id VARCHAR, name VARCHAR, description VARCHAR, color VARCHAR, tags VARCHAR[], website VARCHAR, logo VARCHAR

Notes:
- metrics.source: github, github-traffic, pypi, cran, homebrew, plausible, openvsx, youtube, linkedin, rss
- metrics.metric examples: daily_stars, daily_forks, daily_downloads, total_downloads, daily_pageviews
- events.type: star, fork, issue_open, issue_close, pr_open, pr_merge, comment, reaction
- events.ref: issue/PR number (NULL for stars, forks); join to content for title/details
- events.user: login of the user who performed the action
- events.extra is a JSON object with type-specific fields (e.g. {"value": "thumbs_up"} for reactions)
- content.ref: issue/PR number for GitHub content (join: events.ref = content.ref AND events.target = content.target)
- content.type: entity type (issue, pr, repo, video, post)
- content stores entity data (video listings, blog posts) with upsert-by-id semantics`),
		mcp.WithString("sql", mcp.Required(), mcp.Description("SQL query to execute")),
		mcp.WithNumber("limit", mcp.Description("Maximum rows to return (default: 1000)")),
	)
}

func schemaTool() mcp.Tool {
	return mcp.NewTool("schema",
		mcp.WithDescription("Show column definitions for all DuckDB views: metrics, events, content, projects."),
	)
}

func listProjectsTool() mcp.Tool {
	return mcp.NewTool("list_projects",
		mcp.WithDescription("List all configured projects with their source configurations."),
	)
}

func showProjectTool() mcp.Tool {
	return mcp.NewTool("show_project",
		mcp.WithDescription("Show detailed information about a project including per-source fetch stats."),
		mcp.WithString("id", mcp.Required(), mcp.Description("Project ID")),
	)
}

func badgeTool() mcp.Tool {
	return mcp.NewTool("badge",
		mcp.WithDescription("Generate an SVG badge from metrics data. Types: stars, forks, downloads, pageviews, custom."),
		mcp.WithString("type", mcp.Required(), mcp.Description("Badge type: stars, forks, downloads, pageviews, or custom")),
		mcp.WithString("project", mcp.Description("Scope to a specific project")),
		mcp.WithString("query", mcp.Description("SQL query returning a single value (required for custom type)")),
		mcp.WithString("label", mcp.Description("Override label text (required for custom type)")),
		mcp.WithString("style", mcp.Description("Badge style: flat, flat-square, or plastic")),
		mcp.WithString("color", mcp.Description("Message background color (hex)")),
	)
}

func versionTool() mcp.Tool {
	return mcp.NewTool("version",
		mcp.WithDescription("Show velocirepo version, commit hash, and build date."),
	)
}

func fetchTool() mcp.Tool {
	return mcp.NewTool("fetch", fetchToolOptions("Fetch metrics from all configured sources for all projects.")...)
}

func fetchSourceTool(desc sourceinfo.Descriptor) mcp.Tool {
	return mcp.NewTool(desc.FetchToolName, fetchToolOptions(desc.FetchDescription)...)
}

func fetchToolOptions(description string) []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithDescription(description),
		mcp.WithString("project", mcp.Description("Fetch only this project ID")),
		mcp.WithString("start_date", mcp.Description("Start date (YYYY-MM-DD)")),
		mcp.WithString("end_date", mcp.Description("End date (YYYY-MM-DD, default: yesterday)")),
	}
}

func addProjectTool() mcp.Tool {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Add a new project to the velocirepo config."),
		mcp.WithString("id", mcp.Required(), mcp.Description("Project ID (lowercase alphanumeric with hyphens)")),
		mcp.WithString("name", mcp.Description("Display name (defaults to ID)")),
	}
	for _, desc := range sourceinfo.All() {
		opts = append(opts, mcp.WithString(desc.MCPKey, mcp.Description(desc.MCPAddDescription)))
	}
	return mcp.NewTool("add_project", opts...)
}

func updateProjectTool() mcp.Tool {
	opts := []mcp.ToolOption{
		mcp.WithDescription("Update a project's configuration. Only specified fields are changed."),
		mcp.WithString("id", mcp.Required(), mcp.Description("Project ID to update")),
		mcp.WithString("name", mcp.Description("New display name")),
	}
	for _, desc := range sourceinfo.All() {
		opts = append(opts, mcp.WithString(desc.MCPKey, mcp.Description(desc.MCPUpdateDescription)))
	}
	return mcp.NewTool("update_project", opts...)
}

func removeProjectTool() mcp.Tool {
	return mcp.NewTool("remove_project",
		mcp.WithDescription("Remove a project from the config."),
		mcp.WithString("id", mcp.Required(), mcp.Description("Project ID to remove")),
		mcp.WithBoolean("delete_data", mcp.Description("Also remove the project's data directories")),
	)
}

func renameProjectTool() mcp.Tool {
	return mcp.NewTool("rename_project",
		mcp.WithDescription("Rename a project ID (also moves data directories)."),
		mcp.WithString("old_id", mcp.Required(), mcp.Description("Current project ID")),
		mcp.WithString("new_id", mcp.Required(), mcp.Description("New project ID")),
	)
}

func importProjectsTool() mcp.Tool {
	return mcp.NewTool("import_projects",
		mcp.WithDescription("Bulk-import projects from a GitHub organization or user."),
		mcp.WithString("github_org", mcp.Description("Import repos from this GitHub organization")),
		mcp.WithString("github_user", mcp.Description("Import repos from this GitHub user")),
		mcp.WithString("filter", mcp.Description("Glob pattern to filter repo names")),
		mcp.WithBoolean("skip_existing", mcp.Description("Skip projects that already exist in config")),
	)
}

func validateProjectsTool() mcp.Tool {
	return mcp.NewTool("validate_projects",
		mcp.WithDescription("Verify that configured source URLs are reachable via HTTP HEAD checks."),
		mcp.WithString("project", mcp.Description("Validate only this project")),
	)
}

func exportTool() mcp.Tool {
	return mcp.NewTool("export",
		mcp.WithDescription("Export metrics data to Parquet or CSV files."),
		mcp.WithString("directory", mcp.Required(), mcp.Description("Output directory path")),
		mcp.WithString("format", mcp.Description("Output format: parquet or csv (default: parquet)")),
		mcp.WithString("source", mcp.Description("Export only this source")),
		mcp.WithString("project", mcp.Description("Export only this project")),
	)
}

func migrateTool() mcp.Tool {
	return mcp.NewTool("migrate",
		mcp.WithDescription("Migrate data to the latest schema version."),
		mcp.WithBoolean("force", mcp.Description("Re-run all migrations from scratch")),
	)
}

func listViewsTool() mcp.Tool {
	return mcp.NewTool("list_views",
		mcp.WithDescription("List all configured views with their framework and data source."),
	)
}

func showViewTool() mcp.Tool {
	return mcp.NewTool("show_view",
		mcp.WithDescription("Show details about a specific view."),
		mcp.WithString("name", mcp.Required(), mcp.Description("View name (relative path without extension)")),
	)
}

func addViewTool() mcp.Tool {
	return mcp.NewTool("add_view",
		mcp.WithDescription("Scaffold a new view directory with render.sh and template files."),
		mcp.WithString("name", mcp.Required(), mcp.Description("View name (can include slashes for subdirs, e.g. weekly/stars)")),
		mcp.WithString("framework", mcp.Required(), mcp.Description("Framework: quarto-python, quarto-r, jupyter, marimo, r, sql")),
		mcp.WithString("source", mcp.Description("Data source: duckdb (default) or parquet")),
	)
}

func removeViewTool() mcp.Tool {
	return mcp.NewTool("remove_view",
		mcp.WithDescription("Remove a view directory."),
		mcp.WithString("name", mcp.Required(), mcp.Description("View name to remove")),
	)
}

func renderViewTool() mcp.Tool {
	return mcp.NewTool("render_view",
		mcp.WithDescription("Render a view by running its render.sh script."),
		mcp.WithString("name", mcp.Required(), mcp.Description("View name or directory prefix to render")),
	)
}

func renderViewsTool() mcp.Tool {
	return mcp.NewTool("render_views",
		mcp.WithDescription("Render all views, or those matching a prefix."),
		mcp.WithString("prefix", mcp.Description("Only render views whose name starts with this prefix")),
	)
}
