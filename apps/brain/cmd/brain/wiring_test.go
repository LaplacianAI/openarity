package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// newObjectStore was written with its adapters, its enum value, its validation
// and its own wiring tests across four pull requests, and nothing ever called
// it. Every check stayed green: `unused` assumes an exported identifier has a
// caller in another package, and a constructor in package main exercised only
// by its own tests looks exactly like one in use.
//
// So this walks the package and asserts that every constructor is reachable
// from production code rather than only from a test. A test that calls the
// thing under test cannot tell you whether the program does.
func TestEveryConstructorIsCalledOutsideItsOwnTests(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read cmd/brain: %v", err)
	}

	fset := token.NewFileSet()

	// Declared in a non-test file, and used in one.
	declared := map[string]token.Pos{}
	used := map[string]bool{}

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		for _, decl := range file.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "new") {
				continue
			}
			declared[fn.Name.Name] = fn.Pos()
		}

		// Every identifier in the file except the function names themselves.
		ast.Inspect(file, func(n ast.Node) bool {
			call, isCall := n.(*ast.CallExpr)
			if !isCall {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok {
				used[ident.Name] = true
			}
			return true
		})
	}

	if len(declared) < 3 {
		t.Fatalf("found %d constructors, which means the walk is broken", len(declared))
	}

	for name, pos := range declared {
		if !used[name] {
			t.Errorf("%s is declared at %s and called from no production file. "+
				"It runs in its own tests and never in the program.",
				name, fset.Position(pos))
		}
	}
}
