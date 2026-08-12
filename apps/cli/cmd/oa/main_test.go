package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func noEnv(string) string { return "" }

// Every failure is prefixed, because a bare sentence on stderr is
// indistinguishable from output of whatever else is running in that shell.
func TestTheErrorIsPrefixedWithTheBinaryName(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printError(&buf, noEnv, errors.New("not authenticated"))

	if !strings.HasPrefix(buf.String(), "oa: ") {
		t.Errorf("the error is not prefixed: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "not authenticated") {
		t.Errorf("the message was lost: %q", buf.String())
	}
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Errorf("the error does not end in a newline: %q", buf.String())
	}
}

// The styles are built against the writer being printed to. Redirecting
// stderr to a file must produce a plain sentence — an error is the line most
// likely to be pasted into an issue, and escape sequences make it unreadable.
func TestTheErrorIsPlainOnANonTerminal(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printError(&buf, noEnv, errors.New("not authenticated"))

	if strings.Contains(buf.String(), "\x1b") {
		t.Errorf("an escape sequence reached a non-terminal writer: %q", buf.String())
	}
}

// The theme override has to reach this path too. It is the one output a user
// sees when nothing else worked, so it must not be the one place that queries
// the terminal and stalls.
func TestTheErrorHonoursTheThemeOverride(t *testing.T) {
	t.Parallel()

	env := func(key string) string {
		if key == "OPENARITY_THEME" {
			return "light"
		}
		return ""
	}

	var buf bytes.Buffer
	printError(&buf, env, errors.New("not authenticated"))

	if !strings.Contains(buf.String(), "not authenticated") {
		t.Errorf("the message was lost: %q", buf.String())
	}
}

// A wrapped error can carry a newline — a YAML parse failure does. The
// prefix belongs to the first line, and the rest must not lose it silently by
// being printed as an unrelated block.
func TestAMultiLineErrorKeepsItsPrefix(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printError(&buf, noEnv, errors.New("parse config:\nline 1: bad"))

	first, _, found := strings.Cut(buf.String(), "\n")
	if !found {
		t.Fatalf("expected more than one line: %q", buf.String())
	}
	if !strings.HasPrefix(first, "oa: ") {
		t.Errorf("the first line is not prefixed: %q", first)
	}
}
