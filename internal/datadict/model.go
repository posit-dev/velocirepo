// Package datadict writes a data-dict.yaml file
// (https://data-dict.tidyverse.org) that describes an export: its tables
// and columns from the schema, the meaning of sources, metrics and event
// types from the source metadata, representative values from the data, and
// what an instance adds in [data_dict] of velocirepo.toml.
package datadict

import (
	"bytes"

	"gopkg.in/yaml.v3"
)

// SpecVersion is the data-dict spec version the dictionary conforms to.
const SpecVersion = "0.1.0"

// LearnMore is where readers learn about the format.
const LearnMore = "https://data-dict.tidyverse.org/"

// FileName is the dictionary's file name in an export directory.
const FileName = "data-dict.yaml"

// Dictionary is a data-dict.yaml document.
type Dictionary struct {
	Version       string         `yaml:"$version"`
	LearnMore     string         `yaml:"$learn_more"`
	Name          string         `yaml:"name,omitempty"`
	Label         string         `yaml:"label,omitempty"`
	Description   string         `yaml:"description,omitempty"`
	Details       string         `yaml:"details,omitempty"`
	Origin        string         `yaml:"origin,omitempty"`
	DataVersion   *Version       `yaml:"version,omitempty"`
	Tables        []Table        `yaml:"tables"`
	Relationships []Relationship `yaml:"relationships,omitempty"`
	Glossary      *Glossary      `yaml:"glossary,omitempty"`
}

// Version is the version of the data: the day it runs through.
type Version struct {
	Date string `yaml:"date"`
}

type Table struct {
	Name        string       `yaml:"name"`
	Description string       `yaml:"description,omitempty"`
	Details     string       `yaml:"details,omitempty"`
	Columns     []Column     `yaml:"columns"`
	Definitions []Definition `yaml:"definitions,omitempty"`
}

type Column struct {
	Name        string   `yaml:"name"`
	Type        string   `yaml:"type"`
	Constraints []string `yaml:"constraints,omitempty,flow"`
	TimeZone    string   `yaml:"time_zone,omitempty"`
	Units       string   `yaml:"units,omitempty"`
	Values      *Values  `yaml:"values,omitempty"`
	Range       []any    `yaml:"range,omitempty,flow"`
	// Examples is nil for types without examples; an empty list for a
	// column that's always empty.
	Examples    *Examples `yaml:"examples,omitempty"`
	Description string    `yaml:"description,omitempty"`
	Details     string    `yaml:"details,omitempty"`
	Todo        string    `yaml:"todo,omitempty"`
}

// Examples are representative values of a column.
type Examples []any

func (e Examples) MarshalYAML() (any, error) {
	node := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	for _, v := range e {
		var item yaml.Node
		if err := item.Encode(v); err != nil {
			return nil, err
		}
		node.Content = append(node.Content, &item)
	}
	return node, nil
}

// Values are the values of an enum column with their labels, in order.
type Values []Value

type Value struct {
	Name  string
	Label string
}

func (v Values) MarshalYAML() (any, error) {
	node := &yaml.Node{Kind: yaml.MappingNode}
	for _, value := range v {
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: value.Name, Tag: "!!str"},
			&yaml.Node{Kind: yaml.ScalarNode, Value: value.Label, Tag: "!!str"})
	}
	return node, nil
}

type Definition struct {
	Name        string `yaml:"name"`
	Label       string `yaml:"label,omitempty"`
	Description string `yaml:"description,omitempty"`
	Details     string `yaml:"details,omitempty"`
	Expr        string `yaml:"expr"`
	Todo        string `yaml:"todo,omitempty"`
}

type Relationship struct {
	Join        string `yaml:"join"`
	Cardinality string `yaml:"cardinality"`
	Description string `yaml:"description,omitempty"`
}

// Glossary is a map from term to definition, in order.
type Glossary []Value

func (g Glossary) MarshalYAML() (any, error) {
	return Values(g).MarshalYAML()
}

// YAML returns the dictionary as YAML.
func (d *Dictionary) YAML() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(d); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
