package cli

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/config"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output"
)

// Commands is the tree, supplied by the caller rather than built here. Every
// command package imports this one for Options, so this one importing them
// back would be a cycle — cmd/oa is the composition root and the only place
// that knows the whole list.
type Commands func(*Options) []*cobra.Command

func NewRoot(stdout, stderr io.Writer, commands Commands) *cobra.Command {
	opts := NewOptions(stdout, stderr)

	root := &cobra.Command{
		Use:   "oa",
		Short: "The Openarity CLI",
		Long: "oa talks to a brain over its HTTP API.\n\n" +
			"Against a development brain it needs no setup: run `oa whoami` and it\n" +
			"finds the shared token already in your shell. Anywhere else, `oa login`.",
		SilenceUsage:      true,
		SilenceErrors:     true,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error { return opts.Load() },
	}

	root.PersistentFlags().StringVar(&opts.ServerFlag, "server", "",
		"the brain's API address (default $OPENARITY_SERVER, then the saved config, then "+config.DefaultServer+")")
	root.PersistentFlags().StringVar(&opts.TokenFlag, "token", "",
		"the credential to send, instead of the saved one")
	root.PersistentFlags().BoolVar(&opts.NonInteractive, "non-interactive", false,
		"never prompt; fail instead of asking")
	root.PersistentFlags().StringVarP(&opts.OutputFlag, "output", "o", "",
		"how to print results: "+output.Names()+" (default $OPENARITY_OUTPUT, then the saved config, then "+string(output.Default)+")")

	root.AddCommand(commands(opts)...)

	return root
}
