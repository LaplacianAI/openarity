package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/LaplacianAI/openarity/apps/cli/internal/ui"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		printError(os.Stderr, os.Getenv, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := newRootCmd(stdout, stderr)
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}

func printError(w io.Writer, env ui.Env, err error) {
	fmt.Fprintln(w, ui.New(w, env).Err.Render("oa: "+err.Error()))
}
