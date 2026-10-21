package check

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/posit-dev/velocirepo/internal/source"
	"github.com/posit-dev/velocirepo/internal/store"
)

// Diff says which two versions of the data the diff checks compare.
type Diff struct {
	// Base is a commit.
	Base string
	// Head is a commit, or "" for the work tree.
	Head string
	// Repairs are the scopes in which changes are deliberate. Between two
	// commits, the repair trailers of the commits in between are added.
	Repairs []Repair
}

// Repair is the scope of a repair: a source and project, from one day
// through another.
type Repair struct {
	Source  string
	Project string
	From    string
	To      string
}

func (r Repair) String() string {
	return fmt.Sprintf("%s/%s %s..%s", r.Source, r.Project, r.From, r.To)
}

func (r Repair) covers(sourceName, project, date string) bool {
	return r.Source == sourceName && r.Project == project && (date == "" || (date >= r.From && date <= r.To))
}

var repairPattern = regexp.MustCompile(`^([^/\s]+)/(\S+) (\d{4}-\d{2}-\d{2})\.\.(\d{4}-\d{2}-\d{2})$`)

// ParseRepair parses a repair scope, "<source>/<project> <from>..<to>".
func ParseRepair(s string) (Repair, error) {
	m := repairPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Repair{}, fmt.Errorf("repair %q isn't <source>/<project> <from>..<to>", s)
	}
	return Repair{Source: m[1], Project: m[2], From: m[3], To: m[4]}, nil
}

// RepairTrailer is the trailer that marks a commit as a repair.
const RepairTrailer = "Velocirepo-Repair"

// repairsInMessages returns the repairs named by trailers in commit messages.
func repairsInMessages(messages string) []Repair {
	var out []Repair
	for _, line := range strings.Split(messages, "\n") {
		value, ok := strings.CutPrefix(strings.TrimSpace(line), RepairTrailer+":")
		if !ok {
			continue
		}
		if r, err := ParseRepair(value); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// item is one record, event or content entry, as the diff checks compare
// it.
type item struct {
	target string
	date   string
	// label is the metric, the event type, or the content file.
	label string
	value int64
	// valued is set for records, whose values must not change.
	valued bool
}

// ProjectDiff is how one project's data of one source differs between two
// versions.
type ProjectDiff struct {
	Category string
	Source   string
	Project  string
	// Dir is the project's directory, relative to the data directory.
	Dir     string
	removed []item
	added   []item
	changed []item
	// keys of the added items, for recognising renames.
	addedKeys map[string]bool
	// removedKeys of the removed items, for recognising renames.
	removedKeys []string
}

// Removed, Added and Changed count the project's removed, added and changed
// items.
func (d ProjectDiff) Removed() int { return len(d.removed) }
func (d ProjectDiff) Added() int   { return len(d.added) }
func (d ProjectDiff) Changed() int { return len(d.changed) }

var dataCategories = map[string]bool{store.MetricsDir: true, store.EventsDir: true, store.ContentDataDir: true}

// CompareData compares the data of the projects that changed between the two
// versions of a diff, as sets of keys per project, so that records moving
// between files, as concatenation does, aren't changes. Only record, event and
// content files are read: watermarks, the fetch log and the schema version
// aren't data.
func CompareData(ctx context.Context, dataDir string, d Diff) ([]ProjectDiff, []Repair, error) {
	root, err := topLevel(ctx, dataDir)
	if err != nil {
		return nil, nil, err
	}
	absData, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, nil, err
	}
	if resolved, err := filepath.EvalSymlinks(absData); err == nil {
		absData = resolved
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	rel, err := filepath.Rel(root, absData)
	if err != nil {
		return nil, nil, err
	}
	prefix := filepath.ToSlash(rel)
	g := gitRepo{dir: root}

	repairs := append([]Repair(nil), d.Repairs...)
	if d.Head != "" {
		msgs, err := g.messages(ctx, d.Base, d.Head)
		if err != nil {
			return nil, nil, err
		}
		repairs = append(repairs, repairsInMessages(msgs)...)
	}

	changed, err := g.changedFiles(ctx, d.Base, d.Head, prefix)
	if err != nil {
		return nil, nil, err
	}
	projects := map[string][3]string{}
	for _, f := range changed {
		dir, parts, ok := projectOf(prefix, f)
		if ok {
			projects[dir] = parts
		}
	}
	dirs := make([]string, 0, len(projects))
	for dir := range projects {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	cat, err := g.catFile(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = cat.close() }()

	v := versions{g: g, cat: cat, root: root, prefix: prefix, base: d.Base, head: d.Head}
	if err := v.readSchemaVersions(); err != nil {
		return nil, nil, err
	}

	var out []ProjectDiff
	for _, dir := range dirs {
		parts := projects[dir]
		before, err := v.read(ctx, d.Base, dir, parts[0], true)
		if err != nil {
			return nil, nil, err
		}
		after, err := v.read(ctx, d.Head, dir, parts[0], false)
		if err != nil {
			return nil, nil, err
		}
		pd := ProjectDiff{Category: parts[0], Source: parts[1], Project: parts[2], Dir: strings.TrimPrefix(dir, prefix+"/"), addedKeys: map[string]bool{}}
		for key, b := range before {
			a, ok := after[key]
			switch {
			case !ok:
				pd.removed = append(pd.removed, b)
				pd.removedKeys = append(pd.removedKeys, key)
			case b.valued && a.value != b.value:
				pd.changed = append(pd.changed, b)
			}
		}
		for key, a := range after {
			if _, ok := before[key]; !ok {
				pd.added = append(pd.added, a)
				pd.addedKeys[key] = true
			}
		}
		if len(pd.removed)+len(pd.added)+len(pd.changed) > 0 {
			out = append(out, pd)
		}
	}
	return out, repairs, nil
}

// projectOf returns the project directory a data file is in, and its
// category, source and project.
func projectOf(prefix, file string) (string, [3]string, bool) {
	rest, ok := strings.CutPrefix(file, prefix+"/")
	if !ok {
		return "", [3]string{}, false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 4 || !dataCategories[parts[0]] || !isDataFile(parts[3]) {
		return "", [3]string{}, false
	}
	return path.Join(prefix, parts[0], parts[1], parts[2]), [3]string{parts[0], parts[1], parts[2]}, true
}

func isDataFile(name string) bool {
	return strings.HasSuffix(name, ".jsonl") && !store.IsWatermarkFile(name) && !strings.HasPrefix(name, ".")
}

// versions reads the two versions of the data.
type versions struct {
	g          gitRepo
	cat        *catFile
	root       string
	prefix     string
	base, head string
	// migrateFrom is the base's schema version when the head's is later,
	// so the base is migrated before it's compared; zero otherwise.
	migrateFrom int
	// trees holds each commit's data files by project directory, listed
	// once per commit.
	trees map[string]map[string][]string
}

// filesAt returns the data files of a project directory at a commit.
func (v *versions) filesAt(ctx context.Context, rev, dir string) ([]string, error) {
	if v.trees == nil {
		v.trees = map[string]map[string][]string{}
	}
	tree, ok := v.trees[rev]
	if !ok {
		names, err := v.g.listFiles(ctx, rev, v.prefix)
		if err != nil {
			return nil, err
		}
		tree = map[string][]string{}
		for _, name := range names {
			if d, _, ok := projectOf(v.prefix, name); ok {
				tree[d] = append(tree[d], name)
			}
		}
		v.trees[rev] = tree
	}
	return tree[dir], nil
}

func (v *versions) schemaVersion(rev string) (int, error) {
	name := v.prefix + "/" + store.SchemaVersionFile
	var data []byte
	var err error
	if rev == "" {
		data, err = os.ReadFile(filepath.Join(v.root, filepath.FromSlash(name)))
		if os.IsNotExist(err) {
			return 0, nil
		}
	} else {
		data, err = v.cat.read(rev, name)
	}
	if err != nil || len(data) == 0 {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

func (v *versions) readSchemaVersions() error {
	base, err := v.schemaVersion(v.base)
	if err != nil {
		return err
	}
	head, err := v.schemaVersion(v.head)
	if err != nil {
		return err
	}
	if base > 0 && head > base {
		v.migrateFrom = base
	}
	return nil
}

// read returns the items of a project directory at a commit, or in the work
// tree for "".
func (v *versions) read(ctx context.Context, rev, dir, category string, isBase bool) (map[string]item, error) {
	files := map[string][]byte{}
	if rev == "" {
		entries, err := os.ReadDir(filepath.Join(v.root, filepath.FromSlash(dir)))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || !isDataFile(e.Name()) {
				continue
			}
			if onRead != nil {
				onRead(dir + "/" + e.Name())
			}
			data, err := os.ReadFile(filepath.Join(v.root, filepath.FromSlash(dir), e.Name()))
			if err != nil {
				return nil, err
			}
			files[e.Name()] = data
		}
	} else {
		names, err := v.filesAt(ctx, rev, dir)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			base := path.Base(name)
			data, err := v.cat.read(rev, name)
			if err != nil {
				return nil, err
			}
			files[base] = data
		}
	}
	if isBase && v.migrateFrom > 0 {
		migrated, err := migrateFiles(files, strings.TrimPrefix(dir, v.prefix+"/"), v.migrateFrom)
		if err != nil {
			return nil, err
		}
		files = migrated
	}

	items := map[string]item{}
	for name, data := range files {
		parseItems(category, name, data, items)
	}
	return items, nil
}

// migrateFiles runs the schema migrations on a copy of a project's files, so
// a commit that migrates the data is compared with the base as migrate would
// have left it.
func migrateFiles(files map[string][]byte, relDir string, from int) (map[string][]byte, error) {
	tmp, err := os.MkdirTemp("", "velocirepo-diff-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	dir := filepath.Join(tmp, filepath.FromSlash(relDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return nil, err
		}
	}
	if _, err := store.MigrateFrom(tmp, from); err != nil {
		return nil, fmt.Errorf("migrate the base of %s: %w", relDir, err)
	}
	out := map[string][]byte{}
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !isDataFile(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out[e.Name()] = data
	}
	return out, nil
}

// parseItems adds the items of a data file to items, by key. Lines that
// don't parse are left out; data.integrity reports them.
func parseItems(category, name string, data []byte, items map[string]item) {
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		switch category {
		case store.MetricsDir:
			var r source.Record
			if json.Unmarshal(line, &r) != nil {
				continue
			}
			items[recordKey(r)] = item{target: r.Target, date: r.Date, label: r.Metric, value: r.Value, valued: true}
		case store.EventsDir:
			var e source.Event
			if json.Unmarshal(line, &e) != nil {
				continue
			}
			date := e.Datetime
			if len(date) >= 10 {
				date = date[:10]
			}
			items[eventKey(e)] = item{target: e.Target, date: date, label: e.Type}
		case store.ContentDataDir:
			var c source.ContentEntry
			if json.Unmarshal(line, &c) != nil || c.ID == "" {
				continue
			}
			items[name+"\x00"+c.ID] = item{target: c.Target, label: strings.TrimSuffix(name, ".jsonl")}
		}
	}
}

func extraKey(b *strings.Builder, extra map[string]string) {
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString("\x00" + k + "=" + extra[k])
	}
}

// recordKey identifies a record within its project, without its value.
func recordKey(r source.Record) string {
	var b strings.Builder
	b.WriteString(r.Target + "\x00" + r.Metric + "\x00" + r.Date)
	extraKey(&b, r.Extra)
	return b.String()
}

// eventKey identifies an event within its project. Events have no value, so
// a changed event is a removal and an addition.
func eventKey(e source.Event) string {
	var b strings.Builder
	b.WriteString(e.Target + "\x00" + e.Type + "\x00" + e.Datetime + "\x00" + e.User)
	if e.Ref != nil {
		b.WriteString("\x00#" + strconv.Itoa(*e.Ref))
	}
	extraKey(&b, e.Extra)
	return b.String()
}
