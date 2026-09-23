package skill

import (
	"strings"
	"testing"
)

// The specification's own minimal example.
const minimal = `---
name: skill-name
description: A description of what this skill does and when to use it.
---
`

// Shaped like the skills Anthropic publishes: every optional field, a body
// with headings, code and a link to a bundled file.
const published = `---
name: pdf-processing
description: Extract PDF text, fill forms, merge files. Use when handling PDFs.
license: Apache-2.0
compatibility: Requires pdftk and Python 3.12+
metadata:
  author: example-org
  version: "1.0"
allowed-tools: Bash(pdftk:*) Read
---
# PDF processing

Run ` + "`scripts/extract.py`" + ` to pull the text.

For forms, see [the guide](references/FORMS.md).
`

func mustParse(t *testing.T, doc string) Manifest {
	t.Helper()

	m, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v\n%s", err, doc)
	}
	return m
}

func TestTheSpecsMinimalExampleParses(t *testing.T) {
	t.Parallel()

	m := mustParse(t, minimal)
	if m.Name != "skill-name" || m.Description != "A description of what this skill does and when to use it." {
		t.Errorf("got %+v", m)
	}
	if m.License != nil || m.Compatibility != nil || m.AllowedTools != nil || m.Body != "" {
		t.Errorf("fields that were never written came back set: %+v", m)
	}
}

func TestEveryOptionalFieldIsRead(t *testing.T) {
	t.Parallel()

	m := mustParse(t, published)
	for field, got := range map[string]*string{
		"license":       m.License,
		"compatibility": m.Compatibility,
		"allowed-tools": m.AllowedTools,
	} {
		if got == nil {
			t.Errorf("%s was not read", field)
		}
	}
	if *m.License != "Apache-2.0" || *m.AllowedTools != "Bash(pdftk:*) Read" {
		t.Errorf("got %+v", m)
	}
	if m.Metadata["author"] != "example-org" || m.Metadata["version"] != "1.0" {
		t.Errorf("metadata = %v", m.Metadata)
	}
	if !strings.HasPrefix(m.Body, "# PDF processing\n") || !strings.Contains(m.Body, "references/FORMS.md") {
		t.Errorf("body = %q", m.Body)
	}
}

// A body is markdown, and markdown uses --- as a horizontal rule. Only the
// first closing line ends the frontmatter; everything after is the body's.
func TestARuleInTheBodyIsPartOfTheBody(t *testing.T) {
	t.Parallel()

	m := mustParse(t, "---\nname: a\ndescription: d\n---\nabove\n\n---\n\nbelow\n")
	if m.Body != "above\n\n---\n\nbelow\n" {
		t.Errorf("body = %q", m.Body)
	}
}

// Saved on Windows, every line ends \r\n, and the opening line is "---\r\n".
func TestWindowsLineEndingsAreAccepted(t *testing.T) {
	t.Parallel()

	m := mustParse(t, strings.ReplaceAll(published, "\n", "\r\n"))
	if m.Name != "pdf-processing" || strings.Contains(m.Body, "\r") {
		t.Errorf("got name %q, body %q", m.Name, m.Body)
	}
}

func TestAFileThatEndsWithItsFrontmatterHasAnEmptyBody(t *testing.T) {
	t.Parallel()

	if m := mustParse(t, "---\nname: a\ndescription: d\n---"); m.Body != "" {
		t.Errorf("body = %q", m.Body)
	}
}

// Authors rarely quote a version, and the spec's map is string to string. An
// unquoted scalar is kept as the text that was written.
func TestAnUnquotedMetadataValueIsKeptAsText(t *testing.T) {
	t.Parallel()

	m := mustParse(t, "---\nname: a\ndescription: d\nmetadata:\n  version: 1.0\n  beta: true\n---\n")
	if m.Metadata["version"] != "1.0" || m.Metadata["beta"] != "true" {
		t.Errorf("metadata = %v", m.Metadata)
	}
}

// An absent metadata is an empty map, never nil: it is stored as {} and a nil
// map would marshal as null into a NOT NULL jsonb column.
func TestAbsentMetadataIsAnEmptyMap(t *testing.T) {
	t.Parallel()

	if m := mustParse(t, minimal); m.Metadata == nil || len(m.Metadata) != 0 {
		t.Errorf("metadata = %#v, want an empty map", m.Metadata)
	}
}

func TestTheDescriptionIsTrimmedAndCountedInCharacters(t *testing.T) {
	t.Parallel()

	m := mustParse(t, "---\nname: a\ndescription: \"  "+strings.Repeat("é", 1024)+"  \"\n---\n")
	if got := len([]rune(m.Description)); got != 1024 {
		t.Errorf("description is %d characters, want the 1024 without the padding", got)
	}
}

// Every one of these is a file the spec, Claude Code or the database would
// refuse. Refusing it here is what turns a 500 or a silently broken skill into
// a sentence the author can act on.
func TestAManifestTheSpecForbidsIsRefused(t *testing.T) {
	t.Parallel()

	long := strings.Repeat
	for name, tc := range map[string]struct{ doc, want string }{
		"empty file":              {"", "must start with a ---"},
		"no opening line":         {"name: a\ndescription: d\n", "must start with a ---"},
		"no closing line":         {"---\nname: a\ndescription: d\n", "no closing ---"},
		"not YAML":                {"---\nname: [a\n---\n", "not valid YAML"},
		"a list, not fields":      {"---\n- a\n- b\n---\n", "set of fields"},
		"only a comment":          {"---\n# nothing\n---\n", "set of fields"},
		"a field the spec lacks":  {"---\nname: a\nsummary: d\n---\n", `unknown field "summary" on line 2`},
		"a field set twice":       {"---\nname: a\nname: b\ndescription: d\n---\n", `sets "name" twice`},
		"no name":                 {"---\ndescription: d\n---\n", "no name"},
		"upper case name":         {"---\nname: PDF\ndescription: d\n---\n", "lower-case"},
		"leading hyphen":          {"---\nname: -pdf\ndescription: d\n---\n", "lower-case"},
		"trailing hyphen":         {"---\nname: pdf-\ndescription: d\n---\n", "lower-case"},
		"double hyphen":           {"---\nname: pdf--x\ndescription: d\n---\n", "lower-case"},
		"underscore":              {"---\nname: pdf_x\ndescription: d\n---\n", "lower-case"},
		"65 characters":           {"---\nname: " + long("a", 65) + "\ndescription: d\n---\n", "lower-case"},
		"no description":          {"---\nname: a\n---\n", "no description"},
		"blank description":       {"---\nname: a\ndescription: \"   \"\n---\n", "no description"},
		"description over 1024":   {"---\nname: a\ndescription: " + long("d", 1025) + "\n---\n", "at most 1024"},
		"empty license":           {"---\nname: a\ndescription: d\nlicense: \"\"\n---\n", "license"},
		"blank allowed-tools":     {"---\nname: a\ndescription: d\nallowed-tools: \" \"\n---\n", "allowed-tools"},
		"empty compatibility":     {"---\nname: a\ndescription: d\ncompatibility: \"\"\n---\n", "compatibility"},
		"compatibility over 500":  {"---\nname: a\ndescription: d\ncompatibility: " + long("c", 501) + "\n---\n", "at most 500"},
		"nested metadata":         {"---\nname: a\ndescription: d\nmetadata:\n  author:\n    x: y\n---\n", "frontmatter"},
		"metadata that is a list": {"---\nname: a\ndescription: d\nmetadata: [a, b]\n---\n", "frontmatter"},
		"over 256 KiB":            {"---\nname: a\ndescription: d\n---\n" + long("b", MaxManifestBytes), "256 KiB"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := Parse([]byte(tc.doc))
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to say %q", err, tc.want)
			}
			// What reaches the author is theirs to read. A Go type name in a
			// 400 is an internal leaking and a sentence nobody can act on.
			for _, leak := range []string{"skill.", "frontmatter\"", "in type", "main."} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("err = %q names an internal (%q)", err, leak)
				}
			}
		})
	}
}

// Exactly at the limits is allowed; the refusals above are one past them.
func TestTheLimitsThemselvesAreAllowed(t *testing.T) {
	t.Parallel()

	for name, doc := range map[string]string{
		"64-character name":          "---\nname: " + strings.Repeat("a", 64) + "\ndescription: d\n---\n",
		"1024-character description": "---\nname: a\ndescription: " + strings.Repeat("d", 1024) + "\n---\n",
		"500-character compatibility": "---\nname: a\ndescription: d\ncompatibility: " +
			strings.Repeat("c", 500) + "\n---\n",
		"a name of digits": "---\nname: 123\ndescription: d\n---\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			mustParse(t, doc)
		})
	}

	exact := "---\nname: a\ndescription: d\n---\n"
	exact += strings.Repeat("b", MaxManifestBytes-len(exact))
	mustParse(t, exact)
}
