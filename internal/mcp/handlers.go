package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/posit-dev/velocirepo/internal/badge"
	"github.com/posit-dev/velocirepo/internal/config"
	"github.com/posit-dev/velocirepo/internal/datadict"
	"github.com/posit-dev/velocirepo/internal/fetch"
	"github.com/posit-dev/velocirepo/internal/projectdata"
	"github.com/posit-dev/velocirepo/internal/sourceinfo"
	"github.com/posit-dev/velocirepo/internal/store"
	"github.com/posit-dev/velocirepo/internal/version"
	"github.com/posit-dev/velocirepo/internal/views"
)

type handlers struct {
	cfg *config.Config
}

func (h *handlers) projectInfos() []store.ProjectInfo {
	return h.cfg.ProjectInfos()
}

func (h *handlers) indicatorDefs() []store.IndicatorDef {
	return h.cfg.IndicatorDefs()
}

func (h *handlers) rebuildDB() {
	dataDir := h.cfg.DataDir()
	_ = store.BuildDB(dataDir, h.projectInfos(), h.indicatorDefs())
}

func (h *handlers) cfgFilePath() string {
	if h.cfg != nil && h.cfg.Dir != "" {
		return filepath.Join(h.cfg.Dir, "velocirepo.toml")
	}
	return "velocirepo.toml"
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.NewTextContent(text)},
	}
}

func jsonResult(v any) *mcp.CallToolResult {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errorResult(fmt.Sprintf("marshal result: %v", err))
	}
	return textResult(string(data))
}

func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.NewTextContent(msg)},
		IsError: true,
	}
}

func (h *handlers) handleQuery(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sql, err := req.RequireString("sql")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	limit := req.GetInt("limit", defaultMCPQueryLimit)
	sql, err = prepareMCPQuery(sql, limit)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	results, cols, err := store.QueryLiveRestricted(h.cfg.DataDir(), h.projectInfos(), h.indicatorDefs(), sql)
	if err != nil {
		return errorResult(fmt.Sprintf("query error: %v", err)), nil
	}

	output := struct {
		Columns  []string                 `json:"columns"`
		Rows     []map[string]interface{} `json:"rows"`
		RowCount int                      `json:"row_count"`
	}{
		Columns:  cols,
		Rows:     results,
		RowCount: len(results),
	}

	return jsonResult(output), nil
}

func (h *handlers) handleSchema(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	cols, err := store.SchemaLive(h.cfg.DataDir(), h.projectInfos(), h.indicatorDefs())
	if err != nil {
		return errorResult(fmt.Sprintf("schema error: %v", err)), nil
	}

	type column struct {
		Table  string `json:"table"`
		Column string `json:"column"`
		Type   string `json:"type"`
	}

	result := make([]column, len(cols))
	for i, c := range cols {
		result[i] = column{Table: c.Table, Column: c.Column, Type: c.Type}
	}

	return jsonResult(result), nil
}

func (h *handlers) handleListProjects(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	projects := h.cfg.Projects

	var list []map[string]any
	for id, p := range projects {
		list = append(list, projectOutput(id, p))
	}

	return jsonResult(list), nil
}

func (h *handlers) handleShowProject(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := req.RequireString("id")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	proj, err := h.cfg.GetProject(id)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	dataDir := h.cfg.DataDir()

	type sourceStats struct {
		Source   string `json:"source"`
		LastDate string `json:"last_date,omitempty"`
		Records  int    `json:"records"`
	}

	sources := proj.SourceNames()

	var stats []sourceStats
	for _, src := range sources {
		dir := filepath.Join(dataDir, config.SourceDirPath(src), id)
		ds := store.ScanProjectDir(dir)
		stats = append(stats, sourceStats{
			Source:   src,
			LastDate: ds.LastDate,
			Records:  ds.Records,
		})
	}

	output := projectOutput(id, proj)
	output["sources"] = stats

	return jsonResult(output), nil
}

func projectOutput(id string, proj config.Project) map[string]any {
	output := map[string]any{
		"id":   id,
		"name": proj.Name,
	}
	for _, desc := range sourceinfo.All() {
		if values := proj.SourceValues(desc.Name); len(values) > 0 {
			output[desc.MCPKey] = []string(values)
		}
	}
	return output
}

func (h *handlers) handleBadge(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	badgeType, err := req.RequireString("type")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	project := req.GetString("project", "")
	query := req.GetString("query", "")
	label := req.GetString("label", "")
	style := req.GetString("style", "flat")
	color := req.GetString("color", "")

	if project != "" {
		if _, err := h.cfg.GetProject(project); err != nil {
			return errorResult(err.Error()), nil
		}
	}

	var q string
	if badgeType == "custom" {
		if query == "" {
			return errorResult("--query is required for custom badges"), nil
		}
		if label == "" {
			return errorResult("--label is required for custom badges"), nil
		}
		if _, err := validateMCPQuery(query); err != nil {
			return errorResult(err.Error()), nil
		}
		q = query
		if project != "" {
			q = fmt.Sprintf("SELECT * FROM (%s) AS velocirepo_badge_query WHERE project = %s", q, store.SQLStringLiteral(project))
		}
		if color == "" {
			color = "#007ec6"
		}
	} else {
		preset, ok := badge.Presets[badgeType]
		if !ok {
			return errorResult(fmt.Sprintf("unknown badge type %q (available: stars, forks, downloads, pageviews, custom)", badgeType)), nil
		}
		projectLit := ""
		if project != "" {
			projectLit = store.SQLStringLiteral(project)
		}
		q = preset.BuildQuery(projectLit)
		if label == "" {
			label = preset.Label
		}
		if color == "" {
			color = preset.Color
		}
	}

	results, _, err := store.QueryLiveRestricted(h.cfg.DataDir(), h.projectInfos(), h.indicatorDefs(), q)
	if err != nil {
		return errorResult(fmt.Sprintf("badge query: %v", err)), nil
	}

	msg := "0"
	if len(results) > 0 {
		for _, v := range results[0] {
			msg = badge.FormatValue(v)
			break
		}
	}

	svg := badge.Render(badge.Options{
		Label:      label,
		Message:    msg,
		Color:      color,
		LabelColor: "#555",
		Style:      badge.Style(style),
	})

	return textResult(svg), nil
}

func (h *handlers) handleVersion(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	output := struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		Date    string `json:"date"`
	}{
		Version: version.Version,
		Commit:  version.Commit,
		Date:    version.Date,
	}
	return jsonResult(output), nil
}

func (h *handlers) fetchOpts(req mcp.CallToolRequest) fetch.Options {
	return fetch.Options{
		Project:   req.GetString("project", ""),
		StartDate: req.GetString("start_date", ""),
		EndDate:   req.GetString("end_date", ""),
	}
}

func (h *handlers) handleFetch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	results, err := fetch.All(ctx, h.cfg, fetch.TokensFromEnv(), h.fetchOpts(req))
	if err != nil {
		return errorResult(err.Error()), nil
	}
	return jsonResult(results), nil
}

func (h *handlers) handleFetchSource(ctx context.Context, req mcp.CallToolRequest, sourceName string) (*mcp.CallToolResult, error) {
	results, err := fetch.SourceByName(ctx, h.cfg, fetch.TokensFromEnv(), sourceName, h.fetchOpts(req))
	if err != nil {
		return errorResult(err.Error()), nil
	}
	return jsonResult(results), nil
}

var validIDRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func (h *handlers) handleAddProject(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := req.RequireString("id")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	if !validIDRe.MatchString(id) {
		return errorResult(fmt.Sprintf("invalid project ID %q: must be lowercase alphanumeric with hyphens", id)), nil
	}

	if _, err := h.cfg.GetProject(id); err == nil {
		return errorResult(fmt.Sprintf("project %q already exists", id)), nil
	}

	name := req.GetString("name", id)

	proj := config.Project{Name: name}
	for _, desc := range sourceinfo.All() {
		proj.SetSourceValues(desc.Name, toStringList(req.GetString(desc.MCPKey, "")))
	}

	if err := config.AppendProject(h.cfgFilePath(), id, proj); err != nil {
		return errorResult(fmt.Sprintf("add project: %v", err)), nil
	}

	h.cfg.Projects[id] = proj

	return textResult(fmt.Sprintf("Added project '%s'", id)), nil
}

func (h *handlers) handleUpdateProject(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := req.RequireString("id")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	if _, err := h.cfg.GetProject(id); err != nil {
		return errorResult(err.Error()), nil
	}

	args := req.GetArguments()
	updates := make(map[string]string)
	var unsets []string

	if val, ok := args["name"]; ok {
		str, ok := val.(string)
		if !ok {
			return errorResult("name must be a string"), nil
		}
		if str == "" {
			unsets = append(unsets, "name")
		} else {
			updates["name"] = str
		}
	}

	for _, desc := range sourceinfo.All() {
		val, ok := args[desc.MCPKey]
		if !ok {
			continue
		}
		str, ok := val.(string)
		if !ok {
			return errorResult(fmt.Sprintf("%s must be a string", desc.MCPKey)), nil
		}
		if str == "" {
			unsets = append(unsets, desc.TOMLKey)
		} else {
			updates[desc.TOMLKey] = str
		}
	}

	if len(updates) == 0 && len(unsets) == 0 {
		return errorResult("no changes specified"), nil
	}

	cfgPath := h.cfgFilePath()
	if err := config.UpdateProject(cfgPath, id, updates, unsets); err != nil {
		return errorResult(fmt.Sprintf("update project: %v", err)), nil
	}
	updatedCfg, err := config.Load(cfgPath)
	if err != nil {
		return errorResult(fmt.Sprintf("reload config: %v", err)), nil
	}
	h.cfg = updatedCfg

	return textResult(fmt.Sprintf("Updated project '%s'", id)), nil
}

func (h *handlers) handleRemoveProject(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, err := req.RequireString("id")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	if _, err := h.cfg.GetProject(id); err != nil {
		return errorResult(err.Error()), nil
	}

	deleteData := false
	if args := req.GetArguments(); args != nil {
		if v, ok := args["delete_data"]; ok {
			deleteData, _ = v.(bool)
		}
	}

	var trashRoot string
	var dataMoves []projectdata.Move
	if deleteData {
		var err error
		trashRoot, dataMoves, err = projectdata.TrashProjectDirs(h.cfg.DataDir(), id)
		if err != nil {
			return errorResult(fmt.Sprintf("remove data: %v", err)), nil
		}
	}

	if err := config.RemoveProject(h.cfgFilePath(), id); err != nil {
		if len(dataMoves) > 0 {
			if rollbackErr := projectdata.RollbackMoves(dataMoves); rollbackErr != nil {
				return errorResult(fmt.Sprintf("remove project: %v (rollback data: %v)", err, rollbackErr)), nil
			}
			_ = os.RemoveAll(trashRoot)
		}
		return errorResult(fmt.Sprintf("remove project: %v", err)), nil
	}

	delete(h.cfg.Projects, id)

	if trashRoot != "" {
		if err := os.RemoveAll(trashRoot); err != nil {
			return errorResult(fmt.Sprintf("cleanup removed data: %v", err)), nil
		}
	}

	return textResult(fmt.Sprintf("Removed project '%s'", id)), nil
}

func (h *handlers) handleRenameProject(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	oldID, err := req.RequireString("old_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	newID, err := req.RequireString("new_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	if !validIDRe.MatchString(newID) {
		return errorResult(fmt.Sprintf("invalid project ID %q: must be lowercase alphanumeric with hyphens", newID)), nil
	}

	if _, err := h.cfg.GetProject(oldID); err != nil {
		return errorResult(err.Error()), nil
	}
	if _, err := h.cfg.GetProject(newID); err == nil {
		return errorResult(fmt.Sprintf("project %q already exists", newID)), nil
	}

	dataMoves, err := projectdata.MoveProjectDirs(h.cfg.DataDir(), oldID, newID)
	if err != nil {
		if rollbackErr := projectdata.RollbackMoves(dataMoves); rollbackErr != nil {
			return errorResult(fmt.Sprintf("rename data: %v (rollback: %v)", err, rollbackErr)), nil
		}
		return errorResult(fmt.Sprintf("rename data: %v", err)), nil
	}

	if err := config.RenameSection(h.cfgFilePath(), oldID, newID); err != nil {
		if rollbackErr := projectdata.RollbackMoves(dataMoves); rollbackErr != nil {
			return errorResult(fmt.Sprintf("rename project: %v (rollback data: %v)", err, rollbackErr)), nil
		}
		return errorResult(fmt.Sprintf("rename project: %v", err)), nil
	}

	if proj, ok := h.cfg.Projects[oldID]; ok {
		h.cfg.Projects[newID] = proj
		delete(h.cfg.Projects, oldID)
	}

	return textResult(fmt.Sprintf("Renamed project '%s' → '%s'", oldID, newID)), nil
}

func (h *handlers) handleImportProjects(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	githubOrg := req.GetString("github_org", "")
	githubUser := req.GetString("github_user", "")
	filter := req.GetString("filter", "")
	skipExisting := false
	if args := req.GetArguments(); args != nil {
		if v, ok := args["skip_existing"]; ok {
			skipExisting, _ = v.(bool)
		}
	}

	sources := 0
	if githubOrg != "" {
		sources++
	}
	if githubUser != "" {
		sources++
	}
	if sources == 0 {
		return errorResult("specify github_org or github_user"), nil
	}
	if sources > 1 {
		return errorResult("specify only one of github_org or github_user"), nil
	}

	var endpoint string
	if githubOrg != "" {
		endpoint = "orgs/" + githubOrg + "/repos"
	} else {
		endpoint = "users/" + githubUser + "/repos"
	}

	token := os.Getenv("GITHUB_TOKEN")
	entries, err := fetch.FetchGitHubRepos(ctx, token, endpoint, fetch.ImportOptions{Filter: filter})
	if err != nil {
		return errorResult(fmt.Sprintf("import: %v", err)), nil
	}

	existing := h.cfg.Projects
	var added []string
	cfgPath := h.cfgFilePath()

	for _, entry := range entries {
		if _, exists := existing[entry.ID]; exists {
			if skipExisting {
				continue
			}
			return errorResult(fmt.Sprintf("project %q already exists (use skip_existing)", entry.ID)), nil
		}
		if err := config.AppendProject(cfgPath, entry.ID, entry.Project); err != nil {
			return errorResult(fmt.Sprintf("add %s: %v", entry.ID, err)), nil
		}
		h.cfg.Projects[entry.ID] = entry.Project
		added = append(added, entry.ID)
	}

	return textResult(fmt.Sprintf("Imported %d projects: %s", len(added), strings.Join(added, ", "))), nil
}

func (h *handlers) handleValidateProjects(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	projects := h.cfg.Projects
	if len(projects) == 0 {
		return errorResult("no projects configured"), nil
	}

	projectFilter := req.GetString("project", "")
	if projectFilter != "" {
		p, err := h.cfg.GetProject(projectFilter)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		projects = map[string]config.Project{projectFilter: p}
	}

	token := os.Getenv("GITHUB_TOKEN")
	client := &http.Client{Timeout: 10 * time.Second}
	opts := config.ValidationOptions{
		Client:  client,
		Timeout: 10 * time.Second,
		Token:   token,
	}

	type validationResult struct {
		Project string `json:"project"`
		Source  string `json:"source"`
		Value   string `json:"value"`
		OK      bool   `json:"ok"`
		Error   string `json:"error,omitempty"`
	}

	var results []validationResult
	for id, proj := range projects {
		vResults := config.ValidateProject(ctx, opts, id, proj)
		for _, r := range vResults {
			vr := validationResult{
				Project: id,
				Source:  r.Source,
				Value:   r.Value,
				OK:      r.OK,
			}
			if r.Error != "" {
				vr.Error = r.Error
			}
			results = append(results, vr)
		}
	}

	return jsonResult(results), nil
}

func (h *handlers) handleExport(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	directory, err := req.RequireString("directory")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	outDir, err := h.resolveExportDir(directory)
	if err != nil {
		return errorResult(fmt.Sprintf("invalid export directory: %v", err)), nil
	}

	dataDict := req.GetBool("data_dict", false)
	format := req.GetString("format", "")
	switch {
	case format == "" && dataDict:
		format = "duckdb"
	case format == "":
		format = "parquet"
	case dataDict && format != "duckdb":
		return errorResult("data_dict describes the duckdb format; leave out format or use duckdb"), nil
	}
	source := req.GetString("source", "")
	project := req.GetString("project", "")

	x, err := store.Materialize(store.ExportOptions{
		DataDir:    h.cfg.DataDir(),
		Format:     format,
		Source:     source,
		Project:    project,
		Projects:   h.projectInfos(),
		Indicators: h.indicatorDefs(),
	})
	if err != nil {
		return errorResult(fmt.Sprintf("export: %v", err)), nil
	}
	defer func() { _ = x.Close() }()
	written, err := x.Write(outDir, format)
	if err != nil {
		return errorResult(fmt.Sprintf("export: %v", err)), nil
	}

	var notes []string
	if dataDict {
		dict, warnings, err := datadict.Generate(x, datadict.Options{
			Config: h.cfg, Indicators: h.indicatorDefs(), Source: source, Project: project,
		})
		if err != nil {
			return errorResult(fmt.Sprintf("data dictionary: %v", err)), nil
		}
		data, err := dict.YAML()
		if err != nil {
			return errorResult(fmt.Sprintf("data dictionary: %v", err)), nil
		}
		path := filepath.Join(outDir, datadict.FileName)
		if err := os.WriteFile(path, data, 0644); err != nil {
			return errorResult(fmt.Sprintf("data dictionary: %v", err)), nil
		}
		written = append(written, path)
		notes = warnings
	}

	var files []string
	for _, path := range written {
		files = append(files, filepath.Base(path))
	}

	text := fmt.Sprintf("Exported %d files to %s: %s", len(files), outDir, strings.Join(files, ", "))
	for _, n := range notes {
		text += "\nData dictionary: " + n
	}
	return textResult(text), nil
}

// resolveExportDir resolves and validates an export directory supplied by the
// MCP caller. Only relative paths that stay inside the project directory
// (h.cfg.Dir) are accepted; absolute paths, ".." escapes, and symlinks that
// point outside the project directory are all rejected.
func (h *handlers) resolveExportDir(directory string) (string, error) {
	if filepath.IsAbs(directory) {
		return "", fmt.Errorf("must be a relative path, not an absolute path")
	}
	projectDir, err := filepath.Abs(h.cfg.Dir)
	if err != nil {
		return "", fmt.Errorf("resolve project directory: %w", err)
	}
	resolved := filepath.Clean(filepath.Join(projectDir, directory))
	// filepath.Clean resolves ".." components, so this catches path traversal.
	if resolved != projectDir && !strings.HasPrefix(resolved, projectDir+string(filepath.Separator)) {
		return "", fmt.Errorf("must be inside the project directory")
	}
	// Reject symlinks that point outside the project directory. Compare real
	// paths on both sides: the project directory itself may be reached through
	// a symlink (on macOS /tmp and /var are).
	if _, err := os.Lstat(resolved); err == nil {
		real, err := filepath.EvalSymlinks(resolved)
		if err == nil {
			realProject, err := filepath.EvalSymlinks(projectDir)
			if err != nil {
				realProject = projectDir
			}
			if real != realProject && !strings.HasPrefix(real, realProject+string(filepath.Separator)) {
				return "", fmt.Errorf("resolves via symlink to a path outside the project directory")
			}
		}
	}
	return resolved, nil
}

func (h *handlers) handleMigrate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dataDir := h.cfg.DataDir()

	current, err := store.SchemaVersion(dataDir)
	if err != nil {
		return errorResult(fmt.Sprintf("check schema version: %v", err)), nil
	}

	force := false
	if args := req.GetArguments(); args != nil {
		if v, ok := args["force"]; ok {
			force, _ = v.(bool)
		}
	}

	if force {
		current = 0
	}

	if current >= store.LatestSchemaVersion {
		return textResult(fmt.Sprintf("Data is already at schema version %d (latest)", current)), nil
	}

	applied, err := store.MigrateFrom(dataDir, current)
	if err != nil {
		return errorResult(fmt.Sprintf("migration failed after %d step(s): %v", applied, err)), nil
	}

	return textResult(fmt.Sprintf("Applied %d migration(s), now at schema version %d", applied, store.LatestSchemaVersion)), nil
}

func (h *handlers) handleListViews(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	viewsDir := h.cfg.ViewsDir()
	allViews, err := views.Discover(viewsDir)
	if err != nil {
		return errorResult(fmt.Sprintf("discover views: %v", err)), nil
	}

	type viewEntry struct {
		Name string `json:"name"`
		Dir  string `json:"dir"`
	}

	entries := make([]viewEntry, len(allViews))
	for i, v := range allViews {
		entries[i] = viewEntry{
			Name: v.Name,
			Dir:  v.Dir,
		}
	}

	return jsonResult(entries), nil
}

func (h *handlers) handleShowView(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name, err := req.RequireString("name")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	viewsDir := h.cfg.ViewsDir()
	allViews, err := views.Discover(viewsDir)
	if err != nil {
		return errorResult(fmt.Sprintf("discover views: %v", err)), nil
	}

	v, found := views.FindView(allViews, name)
	if !found {
		return errorResult(fmt.Sprintf("view %q not found", name)), nil
	}

	type viewDetail struct {
		Name string `json:"name"`
		Dir  string `json:"dir"`
	}

	detail := viewDetail{
		Name: v.Name,
		Dir:  v.Dir,
	}

	return jsonResult(detail), nil
}

func (h *handlers) handleAddView(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name, err := req.RequireString("name")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	framework, err := req.RequireString("framework")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	fw, err := views.ParseFramework(framework)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	source := req.GetString("source", "duckdb")
	if source != "duckdb" && source != "parquet" {
		return errorResult(fmt.Sprintf("invalid source %q (use duckdb or parquet)", source)), nil
	}

	viewsDir := h.cfg.ViewsDir()
	viewDir, err := views.ScaffoldDir(viewsDir, name)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	var dbPath, dataDir string
	if source == "duckdb" {
		absViewDir, _ := filepath.Abs(viewDir)
		absDataDir, _ := filepath.Abs(h.cfg.DataDir())
		dbFile := filepath.Join(absDataDir, "velocirepo.duckdb")
		rel, _ := filepath.Rel(absViewDir, dbFile)
		dbPath = filepath.ToSlash(rel)
	} else {
		absViewDir, _ := filepath.Abs(viewDir)
		absDataDir, _ := filepath.Abs(filepath.Join(viewsDir, "_data"))
		rel, _ := filepath.Rel(absViewDir, absDataDir)
		dataDir = filepath.ToSlash(rel)
	}

	dir, err := views.Scaffold(views.ScaffoldOptions{
		ViewsDir:  viewsDir,
		Name:      name,
		Framework: fw,
		Source:    source,
		DBPath:    dbPath,
		DataDir:   dataDir,
	})
	if err != nil {
		return errorResult(fmt.Sprintf("scaffold: %v", err)), nil
	}

	return textResult(fmt.Sprintf("Created view '%s' at %s", name, dir)), nil
}

func (h *handlers) handleRemoveView(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name, err := req.RequireString("name")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	viewsDir := h.cfg.ViewsDir()
	allViews, err := views.Discover(viewsDir)
	if err != nil {
		return errorResult(fmt.Sprintf("discover views: %v", err)), nil
	}

	v, found := views.FindView(allViews, name)
	if !found {
		return errorResult(fmt.Sprintf("view %q not found", name)), nil
	}

	if err := os.RemoveAll(v.Dir); err != nil {
		return errorResult(fmt.Sprintf("remove view directory: %v", err)), nil
	}

	return textResult(fmt.Sprintf("Removed view '%s'", name)), nil
}

func (h *handlers) handleRenderView(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name, err := req.RequireString("name")
	if err != nil {
		return errorResult(err.Error()), nil
	}

	viewsDir := h.cfg.ViewsDir()
	allViews, err := views.Discover(viewsDir)
	if err != nil {
		return errorResult(fmt.Sprintf("discover views: %v", err)), nil
	}

	toRender := views.FindViews(allViews, name)
	if len(toRender) == 0 {
		return errorResult(fmt.Sprintf("no views matching %q", name)), nil
	}

	h.rebuildDB()

	var rendered []string
	for _, v := range toRender {
		if err := views.Render(v); err != nil {
			return errorResult(fmt.Sprintf("render %s: %v", v.Name, err)), nil
		}
		rendered = append(rendered, v.Name)
	}

	return textResult(fmt.Sprintf("Rendered %d view(s): %s", len(rendered), strings.Join(rendered, ", "))), nil
}

func (h *handlers) handleRenderViews(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	viewsDir := h.cfg.ViewsDir()
	allViews, err := views.Discover(viewsDir)
	if err != nil {
		return errorResult(fmt.Sprintf("discover views: %v", err)), nil
	}

	prefix := req.GetString("prefix", "")

	var toRender []views.View
	for _, v := range allViews {
		if prefix == "" || v.Name == prefix || strings.HasPrefix(v.Name, prefix+"/") {
			toRender = append(toRender, v)
		}
	}

	if len(toRender) == 0 {
		return textResult("No views found to render"), nil
	}

	h.rebuildDB()

	var rendered int
	var errors []string
	for _, v := range toRender {
		if err := views.Render(v); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", v.Name, err))
			continue
		}
		rendered++
	}

	msg := fmt.Sprintf("Rendered %d/%d views", rendered, len(toRender))
	if len(errors) > 0 {
		msg += "\nErrors:\n" + strings.Join(errors, "\n")
	}
	return textResult(msg), nil
}

func toStringList(s string) config.StringList {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var result config.StringList
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
