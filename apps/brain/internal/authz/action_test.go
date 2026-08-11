package authz

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Actions are code and roles are data, so this package is the closed vocabulary
// the database is checked against. Two things can rot: a constant that never
// makes it into AllActions, and an action string that no longer looks like one.

// AllActions is maintained by hand, so a new constant can be declared and
// forgotten. Nothing would fail — the action simply would not be checked
// against the database, and a permission naming it would grant nothing.
//
// Parsing the package source is the only way to enumerate constants: reflect
// cannot see them, because constants have no runtime representation.
func TestAllActionsListsEveryConstant(t *testing.T) {
	t.Parallel()

	declared := actionConstants(t)
	if len(declared) == 0 {
		t.Fatal("found no Action constants — the parser is not reading this package")
	}

	listed := map[string]bool{}
	for _, a := range AllActions {
		listed[string(a)] = true
	}

	for name, value := range declared {
		if !listed[value] {
			t.Errorf("%s (%q) is declared but missing from AllActions, so nothing checks it", name, value)
		}
	}
	if len(declared) != len(AllActions) {
		t.Errorf("%d constants declared, AllActions has %d entries", len(declared), len(AllActions))
	}
}

// A duplicate would make the database comparison pass while one action is
// silently absent from the count.
func TestAllActionsHasNoDuplicates(t *testing.T) {
	t.Parallel()

	seen := map[Action]bool{}
	for _, a := range AllActions {
		if seen[a] {
			t.Errorf("%q appears twice in AllActions", a)
		}
		seen[a] = true
	}
}

// The strings are the contract with the database. Every one is "resource:verb",
// lower case, with a single colon — a typo like "agentwrite" or "Agent:Write"
// would still compile and would never match a row.
func TestActionStringsFollowTheConvention(t *testing.T) {
	t.Parallel()

	for _, a := range AllActions {
		s := string(a)
		resource, verb, found := strings.Cut(s, ":")
		switch {
		case !found:
			t.Errorf("%q has no colon, want resource:verb", s)
		case resource == "" || verb == "":
			t.Errorf("%q has an empty half", s)
		case strings.Contains(verb, ":"):
			t.Errorf("%q has more than one colon", s)
		case s != strings.ToLower(s):
			t.Errorf("%q is not lower case", s)
		}
	}
}

// An empty action would match an empty string in the database, which is what a
// bad insert produces.
func TestNoActionIsEmpty(t *testing.T) {
	t.Parallel()

	for i, a := range AllActions {
		if a == "" {
			t.Errorf("AllActions[%d] is empty", i)
		}
	}
}

// actionConstants parses this package's source and returns every `Action`
// constant as name → value.
//
// Parsing is the only way to enumerate constants: reflect cannot see them,
// because a constant has no runtime representation. Files are globbed rather
// than read with parser.ParseDir, which is deprecated as of Go 1.25.
func actionConstants(t *testing.T) map[string]string {
	t.Helper()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list package files: %v", err)
	}

	fset := token.NewFileSet()
	constants := map[string]string{}

	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}

		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || len(value.Values) != len(value.Names) {
					continue
				}
				if ident, ok := value.Type.(*ast.Ident); !ok || ident.Name != "Action" {
					continue
				}
				for i, name := range value.Names {
					lit, ok := value.Values[i].(*ast.BasicLit)
					if !ok {
						continue
					}
					unquoted, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatalf("%s: value of %s is not a string literal: %v", path, name.Name, err)
					}
					constants[name.Name] = unquoted
				}
			}
		}
	}
	return constants
}
