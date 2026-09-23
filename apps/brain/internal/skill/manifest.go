package skill

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	ManifestName     = "SKILL.md"
	MaxManifestBytes = 256 << 10

	maxNameRunes          = 64
	maxDescriptionRunes   = 1024
	maxCompatibilityRunes = 500
)

var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

var knownFields = map[string]bool{
	"name": true, "description": true, "license": true,
	"compatibility": true, "metadata": true, "allowed-tools": true,
}

type Manifest struct {
	Name          string
	Description   string
	License       *string
	Compatibility *string
	Metadata      map[string]string
	AllowedTools  *string
	Body          string
}

type frontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       *string           `yaml:"license"`
	Compatibility *string           `yaml:"compatibility"`
	Metadata      map[string]string `yaml:"metadata"`
	AllowedTools  *string           `yaml:"allowed-tools"`
}

func Parse(data []byte) (Manifest, error) {
	if len(data) > MaxManifestBytes {
		return Manifest{}, fmt.Errorf("SKILL.md is over %d KiB", MaxManifestBytes>>10)
	}

	front, body, err := split(data)
	if err != nil {
		return Manifest{}, err
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(front, &doc); err != nil {
		return Manifest{}, fmt.Errorf("SKILL.md frontmatter is not valid YAML: %w", err)
	}
	if err := checkKeys(&doc); err != nil {
		return Manifest{}, err
	}

	var f frontmatter
	if err := doc.Decode(&f); err != nil {
		return Manifest{}, fmt.Errorf("SKILL.md frontmatter: %w", err)
	}

	m := Manifest{
		Name: f.Name, Description: strings.TrimSpace(f.Description),
		License: f.License, Compatibility: f.Compatibility, AllowedTools: f.AllowedTools,
		Metadata: f.Metadata, Body: body,
	}
	if m.Metadata == nil {
		m.Metadata = map[string]string{}
	}
	return m, validate(m)
}

func split(data []byte) ([]byte, string, error) {
	text := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))

	rest, ok := bytes.CutPrefix(text, []byte("---\n"))
	if !ok {
		return nil, "", errors.New("SKILL.md must start with a --- line opening its frontmatter")
	}

	front, body, ok := bytes.Cut(rest, []byte("\n---\n"))
	if !ok {
		front, ok = bytes.CutSuffix(rest, []byte("\n---"))
		if !ok {
			return nil, "", errors.New("SKILL.md frontmatter has no closing --- line")
		}
	}
	return front, strings.TrimLeft(string(body), "\n"), nil
}

func checkKeys(doc *yaml.Node) error {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return errors.New("SKILL.md frontmatter must be a set of fields, such as name and description")
	}

	fields := doc.Content[0].Content
	seen := make(map[string]bool, len(fields)/2)
	for i := 0; i < len(fields); i += 2 {
		key := fields[i]
		if !knownFields[key.Value] {
			return fmt.Errorf("SKILL.md frontmatter has an unknown field %q on line %d", key.Value, key.Line)
		}
		if seen[key.Value] {
			return fmt.Errorf("SKILL.md frontmatter sets %q twice; the second is on line %d", key.Value, key.Line)
		}
		seen[key.Value] = true
	}
	return nil
}

func validate(m Manifest) error {
	switch {
	case m.Name == "":
		return errors.New("SKILL.md frontmatter has no name")
	case utf8.RuneCountInString(m.Name) > maxNameRunes || !namePattern.MatchString(m.Name):
		return errors.New("name must be 1-64 lower-case letters, digits and single hyphens, not starting or ending with one")
	case m.Description == "":
		return errors.New("SKILL.md frontmatter has no description")
	case utf8.RuneCountInString(m.Description) > maxDescriptionRunes:
		return fmt.Errorf("description must be at most %d characters", maxDescriptionRunes)
	case blank(m.License):
		return errors.New("license, when given, must not be empty")
	case blank(m.AllowedTools):
		return errors.New("allowed-tools, when given, must not be empty")
	case blank(m.Compatibility):
		return errors.New("compatibility, when given, must not be empty")
	case m.Compatibility != nil && utf8.RuneCountInString(*m.Compatibility) > maxCompatibilityRunes:
		return fmt.Errorf("compatibility must be at most %d characters", maxCompatibilityRunes)
	}
	return nil
}

func blank(s *string) bool {
	return s != nil && strings.TrimSpace(*s) == ""
}
