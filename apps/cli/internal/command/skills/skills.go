package skills

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
	"github.com/LaplacianAI/openarity/apps/cli/internal/output/printer"
)

const (
	writePermission = "skill:write"

	// The brain's own limits, so a folder that would be refused is refused
	// before twenty megabytes cross the network. The brain still decides.
	maxFiles      = 200
	maxSkillBytes = 20 << 20
)

func New(opts *cli.Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Upload, import and inspect the skills a team's agents can load",
		Long: "A skill is a directory with SKILL.md at the top, as the Agent Skills\n" +
			"specification lays it out. Its frontmatter is its name and description;\n" +
			"the description is what a model reads when deciding whether to load it.\n\n" +
			"Seeing a team's skills needs only to be in the team. Uploading, importing,\n" +
			"syncing or deleting one needs skill:write.",
	}

	cmd.AddCommand(
		newListCmd(opts),
		newGetCmd(opts),
		newUploadCmd(opts),
		newReplaceCmd(opts),
		newImportCmd(opts),
		newSyncCmd(opts),
		newCatCmd(opts),
		newDeleteCmd(opts),
	)
	return cmd
}

func newListCmd(opts *cli.Options) *cobra.Command {
	var paging cli.Paging

	cmd := &cobra.Command{
		Use:     "list <team>",
		Aliases: []string{"ls"},
		Short:   "List a team's skills",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}

			limit, cursor := paging.Values()
			page, err := cli.Result(api.ListSkillsWithResponse(cmd.Context(), team,
				&client.ListSkillsParams{Limit: limit, Cursor: cursor}))
			if err != nil {
				return err
			}

			return cli.PrintPage(opts, cli.Page[client.SkillSummary]{
				Items:      page.Items,
				NextCursor: page.NextCursor,
				Empty:      "no skills",
				More:       "oa skills list " + args[0],
				Row: func(table *printer.Table, s client.SkillSummary) {
					table.Row(opts.Styles.Value.Render(cli.Safe(s.Name)), cli.Safe(string(s.Source)),
						opts.Styles.Muted.Render(s.ID.String()))
				},
			})
		},
	}

	paging.Flags(cmd)
	return cmd
}

func newGetCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "get <team> <skill>",
		Short: "Show a skill: its frontmatter, where it came from, and its files",
		Long: "Both arguments take a name or an id. The body of SKILL.md is in the\n" +
			"json and yaml output; the table shows what is needed to recognise it.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}
			id, err := cli.ResolveSkill(cmd.Context(), api, team, args[1])
			if err != nil {
				return err
			}

			skill, err := cli.Result(api.GetSkillWithResponse(cmd.Context(), team, id))
			if err != nil {
				return err
			}
			return printSkill(opts, skill, "")
		},
	}
}

func newUploadCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "upload <team> <directory|file.zip>",
		Short: "Upload a skill from a directory or a zip",
		Long: "Needs skill:write in the team. The directory holds SKILL.md, at the top\n" +
			"or inside one folder named as the skill. A .git directory is left out;\n" +
			"everything else is sent, and the brain refuses a path it does not like\n" +
			"rather than cleaning it.\n\n" +
			"    oa skills upload platform ./pdf-processing\n" +
			"    oa skills upload platform pdf-processing.zip",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			contentType, body, err := readSkill(args[1])
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

			skill, err := cli.Created(api.CreateSkillWithBodyWithResponse(cmd.Context(), team, contentType, body))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			return printSkill(opts, skill, "uploaded")
		},
	}
}

func newReplaceCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "replace <team> <skill> <directory|file.zip>",
		Short: "Replace a skill's files with a new directory or zip",
		Long: "Needs skill:write in the team. The whole directory is replaced, and an\n" +
			"imported skill becomes the team's own: it is no longer synced from where\n" +
			"it came from.",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			contentType, body, err := readSkill(args[2])
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
			id, err := cli.ResolveSkill(cmd.Context(), api, team, args[1])
			if err != nil {
				return err
			}

			skill, err := cli.Result(api.ReplaceSkillWithBodyWithResponse(cmd.Context(), team, id, contentType, body))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			return printSkill(opts, skill, "replaced")
		},
	}
}

func newImportCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "import <team> <source>",
		Short: "Import a skill from a GitHub folder or a zip at an allowed address",
		Long: "Needs skill:write in the team. The source is one of:\n\n" +
			"    github:<owner>/<repo>/<path>@<ref>\n" +
			"    https://<an allowed host>/<path>.zip\n\n" +
			"The brain fetches it, so the source is reached from the brain and not\n" +
			"from here. A zip host has to be listed in the brain's\n" +
			"OPENARITY_SKILL_IMPORT_HOSTS. The exact commit or digest is recorded,\n" +
			"and `oa skills sync` fetches it again.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			source := strings.TrimSpace(args[1])
			if source == "" {
				return errors.New("name a source to import from")
			}

			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}

			skill, err := cli.Created(api.ImportSkillWithResponse(cmd.Context(), team,
				client.ImportSkillJSONRequestBody{Source: source}))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			return printSkill(opts, skill, "imported")
		},
	}
}

func newSyncCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:   "sync <team> <skill>",
		Short: "Fetch an imported skill again from where it came from",
		Long: "Needs skill:write in the team. Only an imported skill can be synced; an\n" +
			"uploaded one has nowhere to be fetched from.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}
			id, err := cli.ResolveSkill(cmd.Context(), api, team, args[1])
			if err != nil {
				return err
			}

			skill, err := cli.Result(api.SyncSkillWithResponse(cmd.Context(), team, id))
			if err != nil {
				return cli.Needs(err, writePermission)
			}
			return printSkill(opts, skill, "synced")
		},
	}
}

func newCatCmd(opts *cli.Options) *cobra.Command {
	var raw bool

	cmd := &cobra.Command{
		Use:   "cat <team> <skill> <path>",
		Short: "Print one of a skill's files",
		Long: "Writes the file's bytes to stdout as they are stored, whatever -o says,\n" +
			"so it can be redirected to a file. `oa skills get` lists the paths.\n\n" +
			"To a terminal, a file holding control characters — an escape sequence\n" +
			"that would act on the terminal rather than be read — is refused unless\n" +
			"--raw is given. A skill's files are written by its team or its author.",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}
			id, err := cli.ResolveSkill(cmd.Context(), api, team, args[1])
			if err != nil {
				return err
			}

			res, err := api.GetSkillFileWithResponse(cmd.Context(), team, id, strings.TrimPrefix(args[2], "/"))
			if err != nil {
				return err
			}
			if res.StatusCode() != http.StatusOK {
				return cli.APIError(res.StatusCode(), res.Body)
			}
			return cli.WriteRaw(opts, res.Body, raw)
		},
	}

	cmd.Flags().BoolVar(&raw, "raw", false,
		"print to a terminal even a file holding control characters")
	return cmd
}

func newDeleteCmd(opts *cli.Options) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <team> <skill>",
		Aliases: []string{"rm"},
		Short:   "Delete a skill",
		Long: "Needs skill:write in the team. A skill an agent has is not deleted: take\n" +
			"it from the agent first, so no agent loses a skill without anybody\n" +
			"deciding it should.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := opts.API(cmd.Context())
			if err != nil {
				return err
			}
			team, err := cli.ResolveTeam(cmd.Context(), api, args[0])
			if err != nil {
				return err
			}
			id, err := cli.ResolveSkill(cmd.Context(), api, team, args[1])
			if err != nil {
				return err
			}

			if err := cli.NoContent(api.DeleteSkillWithResponse(cmd.Context(), team, id)); err != nil {
				return cli.Needs(err, writePermission)
			}

			return opts.Out.Print(
				client.SkillSummary{ID: id, TeamID: team, Name: args[1]},
				printer.Options{
					Table: func(table *printer.Table) {
						table.Row(opts.Styles.OK.Render("deleted"),
							opts.Styles.Value.Render(cli.Safe(args[1])),
							opts.Styles.Muted.Render(id.String()))
					},
				})
		},
	}
}

func printSkill(opts *cli.Options, skill *client.Skill, did string) error {
	return opts.Out.Print(skill, printer.Options{
		Table: func(table *printer.Table) {
			if did != "" {
				table.Row(opts.Styles.OK.Render(did), opts.Styles.Value.Render(cli.Safe(skill.Name)),
					opts.Styles.Muted.Render(skill.ID.String()))
			} else {
				table.Row(opts.Styles.Value.Render(cli.Safe(skill.Name)), cli.Safe(string(skill.Source)),
					opts.Styles.Muted.Render(skill.ID.String()))
			}
			table.Row("description", strconv.QuoteToGraphic(skill.Description), "")
			if skill.SourceRef != nil {
				table.Row("source", strconv.QuoteToGraphic(*skill.SourceRef), "")
			}
			table.Row("files", strconv.Itoa(len(skill.Files)+1), "")
			table.Row("", "SKILL.md", "")
			for _, f := range skill.Files {
				table.Row("", strconv.QuoteToGraphic(f.Path), opts.Styles.Muted.Render(strconv.FormatInt(f.Size, 10)+" B"))
			}
		},
	})
}

// readSkill turns what was named on the command line into a request body: a
// zip is sent as it is, and a directory becomes one multipart part per file,
// named by its path, which is what a browser sends for a folder upload.
func readSkill(name string) (contentType string, body io.Reader, err error) {
	info, err := os.Stat(name)
	if err != nil {
		return "", nil, fmt.Errorf("read the skill: %w", err)
	}

	if !info.IsDir() {
		if !strings.EqualFold(filepath.Ext(name), ".zip") {
			return "", nil, fmt.Errorf("%s is a file and not a .zip — name the skill's directory, or a zip of it", name)
		}
		if info.Size() > maxSkillBytes {
			return "", nil, fmt.Errorf("%s is %d bytes, and a skill may be %d", name, info.Size(), maxSkillBytes)
		}
		raw, err := os.ReadFile(name) //nolint:gosec // the path is the one the user named
		if err != nil {
			return "", nil, fmt.Errorf("read the skill: %w", err)
		}
		return "application/zip", bytes.NewReader(raw), nil
	}

	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	files, total := 0, int64(0)

	err = filepath.WalkDir(name, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file — a skill holds files and directories only", p)
		}

		rel, err := filepath.Rel(name, p)
		if err != nil {
			return err
		}
		files++
		if files > maxFiles {
			return fmt.Errorf("%s holds more than %d files, which is more than a skill may", name, maxFiles)
		}

		raw, err := os.ReadFile(p) //nolint:gosec // a file inside the directory the user named
		if err != nil {
			return err
		}
		total += int64(len(raw))
		if total > maxSkillBytes {
			return fmt.Errorf("%s holds more than %d bytes, which is more than a skill may", name, maxSkillBytes)
		}

		part, err := form.CreateFormFile("files", path.Clean(filepath.ToSlash(rel)))
		if err != nil {
			return err
		}
		_, err = part.Write(raw)
		return err
	})
	if err != nil {
		return "", nil, fmt.Errorf("read the skill: %w", err)
	}
	if files == 0 {
		return "", nil, fmt.Errorf("%s is empty — a skill needs at least SKILL.md", name)
	}
	if err := form.Close(); err != nil {
		return "", nil, err
	}

	return form.FormDataContentType(), &buf, nil
}
