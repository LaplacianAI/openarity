package sessions

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

const (
	maxNote         = 1000
	feedbackWrites  = "run:write"
	feedbackCleared = "cleared"
)

// feedbackView is what was done, as a script reads it. Value and Note are
// empty for a cleared one.
type feedbackView struct {
	RunID string `json:"run_id" yaml:"run_id"`
	Value string `json:"value" yaml:"value"`
	Note  string `json:"note" yaml:"note"`
}

func newFeedbackCmd(opts *cli.Options) *cobra.Command {
	var (
		note  string
		clear bool
	)

	cmd := &cobra.Command{
		Use:   "feedback <team> <run> [up|down]",
		Short: "Give, change or remove your thumbs up or down on a run's answer",
		Long: "Your word outranks the judge's on that run, and one is kept per person:\n" +
			"giving it again changes it. Needs run:write in the team. Delegates'\n" +
			"runs take none, and neither does a team with self-improvement off.\n\n" +
			"    oa sessions feedback platform 0b7c3e52-8a41-4f6d-9e2b-5c1a7d3f8e60 down --note \"missed the second ticket\"\n" +
			"    oa sessions feedback platform 0b7c3e52-8a41-4f6d-9e2b-5c1a7d3f8e60 --clear",
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			value, err := feedbackValue(args, clear, cmd.Flags().Changed("note"), note)
			if err != nil {
				return err
			}
			run, err := uuid.Parse(strings.TrimSpace(args[1]))
			if err != nil {
				return fmt.Errorf("%q is not a run id — `oa runs list %s <session>` shows them", args[1], args[0])
			}

			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}

			if clear {
				res, err := api.ClearRunFeedbackWithResponse(cmd.Context(), team, run)
				if err := cli.NoContent(res, err); err != nil {
					return cli.Needs(err, feedbackWrites)
				}
				return printFeedback(opts, feedbackView{RunID: run.String()}, feedbackCleared)
			}

			via := client.RunFeedbackRequestViaCli
			body := client.GiveRunFeedbackJSONRequestBody{Value: value, Via: &via}
			if n := strings.TrimSpace(note); n != "" {
				body.Note = &n
			}
			saved, err := cli.Result(api.GiveRunFeedbackWithResponse(cmd.Context(), team, run, body))
			if err != nil {
				return cli.Needs(err, feedbackWrites)
			}
			return printFeedback(opts, feedbackView{
				RunID: run.String(), Value: string(saved.Value), Note: saved.Note,
			}, "thumbs "+string(saved.Value))
		},
	}

	cmd.Flags().StringVar(&note, "note", "", "what was wrong or right, at most 1,000 characters")
	cmd.Flags().BoolVar(&clear, "clear", false, "remove your feedback instead of giving it")
	return cmd
}

// feedbackValue checks everything that can be checked without the brain: one
// of up, down or --clear, and a note that fits.
func feedbackValue(args []string, clear, noteGiven bool, note string) (client.RunFeedbackRequestValue, error) {
	given := len(args) == 3
	switch {
	case clear && given:
		return "", errors.New("--clear removes your feedback; give up or down, or --clear, not both")
	case !clear && !given:
		return "", errors.New("say up or down, or use --clear to remove yours")
	case clear && noteGiven:
		return "", errors.New("--note goes with up or down; --clear has nothing to attach it to")
	case utf8.RuneCountInString(note) > maxNote:
		return "", fmt.Errorf("--note is at most %d characters", maxNote)
	case clear:
		return "", nil
	}

	switch v := client.RunFeedbackRequestValue(args[2]); v {
	case client.RunFeedbackRequestValueUp, client.RunFeedbackRequestValueDown:
		return v, nil
	default:
		return "", fmt.Errorf("%q is not a verdict — say up or down", args[2])
	}
}

func printFeedback(opts *cli.Options, v feedbackView, did string) error {
	return opts.Out.Print(v, printer.Options{
		Table: func(table *printer.Table) {
			table.Row(opts.Styles.OK.Render(did), opts.Styles.Muted.Render(v.RunID))
			if v.Note != "" {
				table.Row("note", cli.Safe(v.Note))
			}
		},
	})
}
