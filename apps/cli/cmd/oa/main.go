package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/command/channels"
	"github.com/LaplacianAI/openarity/apps/cli/internal/command/config"
	cmdcontext "github.com/LaplacianAI/openarity/apps/cli/internal/command/context"
	"github.com/LaplacianAI/openarity/apps/cli/internal/command/login"
	"github.com/LaplacianAI/openarity/apps/cli/internal/command/logout"
	"github.com/LaplacianAI/openarity/apps/cli/internal/command/sessions"
	"github.com/LaplacianAI/openarity/apps/cli/internal/command/teams"
	"github.com/LaplacianAI/openarity/apps/cli/internal/command/users"
	"github.com/LaplacianAI/openarity/apps/cli/internal/command/whoami"
	"github.com/LaplacianAI/openarity/apps/cli/internal/theme"
	"github.com/LaplacianAI/openarity/apps/cli/internal/ui"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		printError(os.Stderr, os.Getenv("OPENARITY_THEME"), err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := cli.NewRoot(stdout, stderr, commands)
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}

func commands(opts *cli.Options) []*cobra.Command {
	return []*cobra.Command{
		whoami.New(opts),
		config.New(opts),
		cmdcontext.New(opts),
		teams.New(opts),
		channels.New(opts),
		sessions.New(opts),
		users.New(opts),
		login.New(opts),
		logout.New(opts),
	}
}

func printError(w io.Writer, themeName string, err error) {
	chosenTheme, _ := theme.Parse(themeName)
	fmt.Fprintln(w, ui.New(w, chosenTheme).Err.Render("oa: "+err.Error()))
}
