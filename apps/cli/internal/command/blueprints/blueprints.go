// Package blueprints is oa plan and oa apply: a team's MCP servers, skills
// and agents described in one file, compared with the team, then written.
package blueprints

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/command/skills"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

const maxFile = 1 << 20

const example = "    # team.yaml\n" +
	"    mcp_servers:\n" +
	"      - name: helpdesk\n" +
	"        url: https://helpdesk.example/mcp\n" +
	"        auth_secret: helpdesk#token   # a secret's name#key in the team, never the secret\n" +
	"    skills:\n" +
	"      - path: ./skills/support-faq    # its folder, beside this file; SKILL.md names it\n" +
	"    agents:\n" +
	"      - name: triage\n" +
	"        model: {name: anthropic/claude-sonnet-5-5, max_tokens: 4096}\n" +
	"        pattern: react\n" +
	"        max_steps: 20\n" +
	"        instructions: Read the email, decide the queue, answer from the FAQ.\n" +
	"        skills: [support-faq]         # by name: in this file, or already in the team\n" +
	"        mcp_servers: [{name: helpdesk, allow: [create_ticket]}]\n"

const rules = "An item the file names is described whole, as a replace is: an agent with\n" +
	"no skills: line loses its skills. An item it does not name is left alone;\n" +
	"nothing is ever removed. Names are the keys, so applying a file twice\n" +
	"changes nothing the second time."

func NewPlan(opts *cli.Options) *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:   "plan <team> -f <file>",
		Short: "Show what applying a blueprint would change in a team",
		Long: "A blueprint is a team's MCP servers, skills and agents in one YAML or\n" +
			"JSON file. plan compares it with the team and writes nothing, so belonging\n" +
			"to the team is enough.\n\n" + rules + "\n\n" + example + "\n" +
			"    oa plan support -f team.yaml",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bp, err := read(name, cmd.InOrStdin())
			if err != nil {
				return err
			}
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}

			plan, err := cli.Result(api.PlanBlueprintWithResponse(cmd.Context(), team, bp))
			if err != nil {
				return err
			}
			return show(opts, plan)
		},
	}

	cmd.Flags().StringVarP(&name, "file", "f", "", "the blueprint as YAML or JSON, or - for stdin")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func NewApply(opts *cli.Options) *cobra.Command {
	var (
		name string
		yes  bool
	)

	cmd := &cobra.Command{
		Use:   "apply <team> -f <file>",
		Short: "Make a team what a blueprint says",
		Long: "Shows the plan, asks, then applies exactly that plan: if the team changed\n" +
			"in between, nothing is written and the new plan is shown. Each write needs\n" +
			"its own permission: tool:write for an MCP server, skill:write for a skill,\n" +
			"agent:write for an agent. Everything is written together or not at all.\n" +
			"MCP servers are not discovered and no secret is written: run\n" +
			"oa mcp-servers discover afterwards, and put secrets in first.\n\n" + rules + "\n\n" +
			"    oa apply support -f team.yaml\n" +
			"    oa apply support -f team.yaml --yes   # no question, for a script",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			table := isTable(opts)
			if (opts.NonInteractive || !table) && !yes {
				return errors.New("apply asks before it writes, showing the plan as a table — pass --yes to skip that")
			}
			bp, err := read(name, cmd.InOrStdin())
			if err != nil {
				return err
			}
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}

			plan, err := cli.Result(api.PlanBlueprintWithResponse(cmd.Context(), team, bp))
			if err != nil {
				return err
			}
			if table || len(plan.Refusals) > 0 || !changes(plan) {
				if err := show(opts, plan); err != nil {
					return err
				}
			}
			if len(plan.Refusals) > 0 {
				return fmt.Errorf("fix the %s first; nothing was applied", count(len(plan.Refusals), "refusal"))
			}
			if !changes(plan) {
				return nil
			}
			if !yes {
				if err := confirm(cmd, opts, args[0], name == "-"); err != nil {
					return err
				}
			}

			res, err := api.ApplyBlueprintWithResponse(cmd.Context(), team,
				client.BlueprintApply{Blueprint: bp, PlanDigest: plan.Digest})
			if err != nil {
				return err
			}
			return applied(opts, args[0], res)
		},
	}

	cmd.Flags().StringVarP(&name, "file", "f", "", "the blueprint as YAML or JSON, or - for stdin")
	cmd.Flags().BoolVar(&yes, "yes", false, "apply without asking")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

// isTable is whether a plan can be shown before the question. Under json or
// yaml, apply prints one document: what it did, or the plan that stopped it.
func isTable(opts *cli.Options) bool {
	f, ok := output.Parse(opts.Settings.Output.Value)
	return !ok || f == output.Table
}

// confirm asks on stderr, so the question is seen under -o json too. A
// blueprint read from stdin leaves no stdin to answer on.
func confirm(cmd *cobra.Command, opts *cli.Options, team string, fromStdin bool) error {
	if fromStdin {
		return errors.New("the blueprint came on stdin, so there is nothing left to answer on — pass --yes")
	}
	fmt.Fprintf(opts.Stderr, "Apply these changes to team %s? [y/N] ", cli.Line(team))

	line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return errors.New("nothing was applied")
}

func applied(opts *cli.Options, team string, res *client.ApplyBlueprintResponse) error {
	switch {
	case res.JSON200 != nil:
		created, changed, _ := tally(res.JSON200.Changes)
		if err := opts.Out.Print(res.JSON200, printer.Options{}); err != nil {
			return err
		}
		opts.Out.Note(opts.Styles.OK.Render("applied") + " " + fmt.Sprintf("%d created, %d changed.", created, changed))
		discovered(opts, team, res.JSON200.Discovered)
		return nil
	case res.JSON409 != nil:
		if err := show(opts, res.JSON409); err != nil {
			return err
		}
		return errors.New("the team changed since that plan, so nothing was applied — the plan above is what it would do now; run apply again")
	case res.JSON422 != nil:
		if err := show(opts, res.JSON422); err != nil {
			return err
		}
		return errors.New("the plan has refusals, so nothing was applied")
	case res.StatusCode() == http.StatusForbidden:
		return cli.Needs(cli.APIError(res.StatusCode(), res.Body), "the permission for each write")
	}
	return cli.APIError(res.StatusCode(), res.Body)
}

// discovered says what each server the apply wrote answered when asked for
// its tools, and how to ask again one that could not be.
func discovered(opts *cli.Options, team string, servers []client.BlueprintDiscovered) {
	s := opts.Styles
	for _, d := range servers {
		name := cli.Line(d.Server)
		if d.Error == nil {
			opts.Out.Note(s.OK.Render("discovered") + " " + fmt.Sprintf("%s: %s.", name, count(len(d.Tools), "tool")))
			continue
		}
		opts.Out.Note(s.Warn.Render("not discovered") + " " + name + ": " + cli.Line(*d.Error))
		opts.Out.Note(s.Muted.Render("  Once that is fixed: oa mcp-servers discover " + cli.Line(team) + " " + name))
	}
}

func changes(p *client.BlueprintPlan) bool {
	for _, c := range p.Changes {
		if c.Action != client.Unchanged {
			return true
		}
	}
	return false
}

var nouns = map[client.BlueprintChangeKind]string{
	client.BlueprintChangeKindMcpServer: "mcp server",
	client.BlueprintChangeKindSkill:     "skill",
	client.BlueprintChangeKindAgent:     "agent",
}

// show prints a plan: a row per item and per refusal, then the totals. Names
// and messages are the team's own text, so they are made safe for a terminal.
func show(opts *cli.Options, p *client.BlueprintPlan) error {
	created, changed, same := tally(p.Changes)

	err := opts.Out.Print(p, printer.Options{
		Table: func(t *printer.Table) {
			s := opts.Styles
			for _, c := range p.Changes {
				noun, name := nouns[c.Kind], cli.Line(c.Name)
				switch c.Action {
				case client.Create:
					t.Row(s.OK.Render("+"), noun, s.Value.Render(name), s.OK.Render("create"))
				case client.Update:
					t.Row(s.Warn.Render("~"), noun, s.Value.Render(name),
						s.Warn.Render("update: "+cli.Line(strings.Join(deref(c.Fields), ", "))))
				case client.Unchanged:
					t.Row(" ", s.Muted.Render(noun), s.Muted.Render(name), s.Muted.Render("unchanged"))
				default:
					t.Row("?", noun, s.Value.Render(name), cli.Line(string(c.Action)))
				}
			}
			for _, r := range p.Refusals {
				t.Row(s.Err.Render("!"), nouns[client.BlueprintChangeKind(r.Kind)],
					s.Value.Render(cli.Line(r.Name)), s.Err.Render(cli.Line(r.Message)))
			}
		},
	})
	if err != nil {
		return err
	}

	if created+changed == 0 && len(p.Refusals) == 0 {
		opts.Out.Note("Nothing to change.")
		return nil
	}
	opts.Out.Note(fmt.Sprintf("%d to create, %d to change, %d unchanged.", created, changed, same))
	return nil
}

func tally(changes []client.BlueprintChange) (created, changed, same int) {
	for _, c := range changes {
		switch c.Action {
		case client.Create:
			created++
		case client.Update:
			changed++
		case client.Unchanged:
			same++
		}
	}
	return created, changed, same
}

func deref(s *[]string) []string {
	if s == nil {
		return nil
	}
	return *s
}

func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// file is a blueprint as written: each skill a folder, read here and sent as
// its files.
type file struct {
	MCPServers []client.BlueprintMCPServer `yaml:"mcp_servers"`
	Skills     []skillPath                 `yaml:"skills"`
	Agents     []client.BlueprintAgent     `yaml:"agents"`
}

type skillPath struct {
	Path string `yaml:"path"`
}

// read turns the file into the request: YAML, or JSON, which is YAML. A key
// the format does not have is refused, so a misspelt field is not silently
// left out. A skill's path is taken from the file's own folder.
func read(name string, in io.Reader) (client.Blueprint, error) {
	var raw []byte
	var err error
	if name == "-" {
		raw, err = io.ReadAll(io.LimitReader(in, maxFile+1))
	} else {
		raw, err = os.ReadFile(name) //nolint:gosec // the path is the one the user named
	}
	if err != nil {
		return client.Blueprint{}, fmt.Errorf("read the blueprint: %w", err)
	}
	if len(raw) > maxFile {
		return client.Blueprint{}, fmt.Errorf("the blueprint is over %d bytes — is that the right file?", maxFile)
	}

	var f file
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return client.Blueprint{}, fmt.Errorf("read the blueprint: %w", err)
	}

	base := "."
	if name != "-" {
		base = filepath.Dir(name)
	}
	bp := client.Blueprint{}
	if f.MCPServers != nil {
		bp.McpServers = &f.MCPServers
	}
	if f.Agents != nil {
		bp.Agents = &f.Agents
	}
	if len(f.Skills) == 0 {
		return bp, nil
	}

	skillsOut := make([]client.BlueprintSkill, len(f.Skills))
	for i, s := range f.Skills {
		if s.Path == "" {
			return client.Blueprint{}, fmt.Errorf("skill %d has no path: name the folder that holds its SKILL.md", i+1)
		}
		dir := s.Path
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(base, dir)
		}
		err := skills.Walk(dir, func(rel string, data []byte) error {
			skillsOut[i].Files = append(skillsOut[i].Files, client.BlueprintSkillFile{Path: rel, Data: data})
			return nil
		})
		if err != nil {
			return client.Blueprint{}, err
		}
	}
	bp.Skills = &skillsOut
	return bp, nil
}
