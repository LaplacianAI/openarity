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

// A command that parse accepts and execute has no case for is not a compile
// error — Go is happy with an unreferenced function, and the routing function
// answers "unhandled command" at runtime. The constructor test above only
// looks at new* names, so runWorker would have slipped past it.
//
// So this reads both halves of the same file set: every commandName constant
// declared, and every one named in a case clause inside execute.
func TestEveryCommandIsRoutedFromExecute(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read cmd/brain: %v", err)
	}

	fset := token.NewFileSet()
	commands := map[string]token.Pos{}
	routed := map[string]bool{}
	found := false

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
			gen, isGen := decl.(*ast.GenDecl)
			if !isGen || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				if ident, ok := value.Type.(*ast.Ident); !ok || ident.Name != "commandName" {
					continue
				}
				for _, n := range value.Names {
					commands[n.Name] = n.Pos()
				}
			}
		}

		// Only the switch that routes a parsed command, found by what it
		// switches on rather than by the name of the function holding it.
		//
		// The first version of this test read case clauses anywhere in the
		// package, and passed with the commandWorker case deleted from
		// execute — because parse has a case for it too. A guard that counts
		// the wrong switch is a guard that certifies nothing.
		ast.Inspect(file, func(n ast.Node) bool {
			sw, isSwitch := n.(*ast.SwitchStmt)
			if !isSwitch || !switchesOnCommandName(sw.Tag) {
				return true
			}
			found = true
			for _, stmt := range sw.Body.List {
				clause, isCase := stmt.(*ast.CaseClause)
				if !isCase {
					continue
				}
				for _, expr := range clause.List {
					if ident, ok := expr.(*ast.Ident); ok {
						routed[ident.Name] = true
					}
				}
			}
			return true
		})
	}

	if len(commands) == 0 {
		t.Fatal("no commandName constants found, so this test proves nothing")
	}
	// Renaming execute, or changing what it switches on, must fail here rather
	// than quietly leave every command unchecked.
	if !found {
		t.Fatal("no switch on a parsed command's name was found in cmd/brain, " +
			"so nothing below was actually checked")
	}

	for name, pos := range commands {
		if !routed[name] {
			t.Errorf("%s is declared at %s and appears in no case clause.\n"+
				"parse accepts it and execute answers \"unhandled command\".",
				name, fset.Position(pos))
		}
	}
}

// switchesOnCommandName recognises the routing switch by its subject: a
// selector like cmd.name, whatever the surrounding function is called.
func switchesOnCommandName(tag ast.Expr) bool {
	sel, ok := tag.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "name"
}
