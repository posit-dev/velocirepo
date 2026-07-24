package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/BurntSushi/toml"
	"github.com/posit-dev/velocirepo/internal/sourceinfo"
	"github.com/posit-dev/velocirepo/internal/store"
)

// StringList is a TOML type that accepts either a single string or an array of strings.
type StringList []string

func (s *StringList) UnmarshalTOML(data interface{}) error {
	switch v := data.(type) {
	case string:
		*s = StringList{v}
	case []interface{}:
		for _, item := range v {
			str, ok := item.(string)
			if !ok {
				return fmt.Errorf("expected string in array, got %T", item)
			}
			*s = append(*s, str)
		}
	default:
		return fmt.Errorf("expected string or array, got %T", data)
	}
	return nil
}

func (s StringList) IsEmpty() bool {
	return len(s) == 0
}

func (s StringList) First() string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

func (s StringList) String() string {
	if len(s) == 0 {
		return ""
	}
	if len(s) == 1 {
		return s[0]
	}
	result := s[0]
	for _, v := range s[1:] {
		result += ", " + v
	}
	return result
}

type Project struct {
	Name        string     `toml:"name"`
	Description string     `toml:"description"`
	Color       string     `toml:"color"`
	Tags        StringList `toml:"tags"`
	Website     string     `toml:"website"`
	Logo        string     `toml:"logo"`

	GitHubEvents  StringList `toml:"github"`
	GitHubTraffic StringList `toml:"github-traffic"`
	PyPI          StringList `toml:"pypi"`
	CRAN          StringList `toml:"cran"`
	Homebrew      StringList `toml:"homebrew"`
	Plausible     StringList `toml:"plausible"`
	OpenVSX       StringList `toml:"openvsx"`
	YouTube       StringList `toml:"youtube"`
	LinkedIn      StringList `toml:"linkedin"`
	RSS           StringList `toml:"rss"`
}

type SourceEntry struct {
	sourceinfo.Descriptor
	Values StringList
}

var stringListType = reflect.TypeOf(StringList{})

func (p Project) Sources() []SourceEntry {
	descriptors := sourceinfo.All()
	entries := make([]SourceEntry, 0, len(descriptors))
	for _, d := range descriptors {
		entries = append(entries, SourceEntry{
			Descriptor: d,
			Values:     p.SourceValues(d.Name),
		})
	}
	return entries
}

func (p Project) SourceValues(name string) StringList {
	desc, ok := sourceinfo.Get(name)
	if !ok || desc.ConfigField == "" {
		return nil
	}
	field := reflect.ValueOf(p).FieldByName(desc.ConfigField)
	if !field.IsValid() || field.Type() != stringListType {
		return nil
	}
	return field.Interface().(StringList)
}

func (p *Project) SetSourceValues(name string, values StringList) bool {
	desc, ok := sourceinfo.Get(name)
	if !ok || desc.ConfigField == "" {
		return false
	}
	field := reflect.ValueOf(p).Elem().FieldByName(desc.ConfigField)
	if !field.IsValid() || !field.CanSet() || field.Type() != stringListType {
		return false
	}
	field.Set(reflect.ValueOf(values))
	return true
}

func (p Project) SourceNames() []string {
	var names []string
	for _, s := range p.Sources() {
		if !s.Values.IsEmpty() {
			names = append(names, s.Name)
		}
	}
	return names
}

func SourceDirPath(sourceName string) string {
	return sourceinfo.DataDirPath(sourceName)
}

func SourceDirNames() []string {
	return sourceinfo.DataDirPaths()
}

type DataConfig struct {
	Dir string `toml:"dir"`
}

type SettingsConfig struct {
	EndDate                  string `toml:"end_date"`
	IncludeDefaultIndicators *bool  `toml:"include_default_indicators"`
}

type ViewsConfig struct {
	Dir string `toml:"dir"`
}

type IndicatorConfig struct {
	Description string `toml:"description"`
	Query       string `toml:"query"`
}

type Config struct {
	Data       DataConfig                 `toml:"data"`
	Settings   SettingsConfig             `toml:"settings"`
	Views      ViewsConfig                `toml:"views"`
	Indicators map[string]IndicatorConfig `toml:"indicators"`
	Projects   map[string]Project         `toml:"projects"`

	// Computed fields
	Dir string `toml:"-"`
}

func (c *Config) ViewsDir() string {
	dir := c.Views.Dir
	if dir == "" {
		dir = "velocirepo/views"
	}
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(c.Dir, dir)
}

func (c *Config) DataDir() string {
	dir := c.Data.Dir
	if dir == "" {
		dir = "velocirepo/data"
	}
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(c.Dir, dir)
}

func (c *Config) GetProject(id string) (Project, error) {
	p, ok := c.Projects[id]
	if !ok {
		return Project{}, fmt.Errorf("project %q not found in config", id)
	}
	return p, nil
}

func (c *Config) ProjectInfos() []store.ProjectInfo {
	if len(c.Projects) == 0 {
		return nil
	}
	infos := make([]store.ProjectInfo, 0, len(c.Projects))
	for id, p := range c.Projects {
		infos = append(infos, store.ProjectInfo{
			ID:          id,
			Name:        p.Name,
			Description: p.Description,
			Color:       p.Color,
			Tags:        []string(p.Tags),
			Website:     p.Website,
			Logo:        p.Logo,
		})
	}
	return infos
}

func (c *Config) IndicatorDefs() []store.IndicatorDef {
	if len(c.Indicators) == 0 {
		return store.DefaultIndicators
	}

	includeDefaults := c.Settings.IncludeDefaultIndicators == nil || *c.Settings.IncludeDefaultIndicators

	var defs []store.IndicatorDef
	if includeDefaults {
		defs = append(defs, store.DefaultIndicators...)
	}
	for name, ind := range c.Indicators {
		defs = append(defs, store.IndicatorDef{
			Name:        name,
			Description: ind.Description,
			Query:       ind.Query,
		})
	}
	return defs
}

func Load(path string) (*Config, error) {
	if path == "" {
		path = os.Getenv("VELOCIREPO_CONFIG")
	}
	if path == "" {
		var err error
		path, err = discover()
		if err != nil {
			return nil, err
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	cfg.Dir = filepath.Dir(path)
	return &cfg, nil
}

func discover() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}

	for {
		path := filepath.Join(dir, "velocirepo.toml")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("velocirepo.toml not found (searched from working directory to root)")
		}
		dir = parent
	}
}
