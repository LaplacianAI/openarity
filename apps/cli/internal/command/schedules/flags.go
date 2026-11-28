package schedules

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/LaplacianAI/openarity/apps/cli/internal/cli"
	"github.com/LaplacianAI/openarity/apps/cli/internal/client"
)

// What the schedule and watcher commands share: how a time, a length, a
// place, an agent and an objective are read from flags. The brain checks
// each again; reading them here is so a mistake is said before anything is
// sent, in the words of the flag that made it.

// maxObjective is the brain's own cap on an objective, in characters.
const maxObjective = 4000

// wallTime is how --at reads a time in a zone.
const wallTime = "2006-01-02 15:04"

// Length reads 30m, 1h, 1d or 1w, or any Go duration, as whole seconds: the
// brain takes no part of a minute.
func Length(flag, v string) (int64, error) {
	v = strings.TrimSpace(v)
	d, err := lengthOf(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("--%s is a length such as 30m, 1h, 1d or 1w, not %q", flag, v)
	}
	if d%time.Minute != 0 {
		return 0, fmt.Errorf("--%s is in whole minutes, not %q", flag, v)
	}
	return int64(d / time.Second), nil
}

func lengthOf(v string) (time.Duration, error) {
	for suffix, unit := range map[string]time.Duration{"d": 24 * time.Hour, "w": 7 * 24 * time.Hour} {
		if n, ok := strings.CutSuffix(v, suffix); ok {
			k, err := strconv.Atoi(n)
			if err != nil {
				return 0, err
			}
			return time.Duration(k) * unit, nil
		}
	}
	return time.ParseDuration(v)
}

// WhenFlags are --at, --every and --cron.
type WhenFlags struct{ At, Every, Cron string }

func (w *WhenFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&w.At, "at", "", "once, at this time: RFC 3339, or YYYY-MM-DD HH:MM in --tz")
	cmd.Flags().StringVar(&w.Every, "every", "", "repeatedly, this often: 30m, 1h, 1d or 1w")
	cmd.Flags().StringVar(&w.Cron, "cron", "", "on a cron rule, five fields or @daily, read in --tz")
}

// given is how many of the three were set.
func (w WhenFlags) given() int {
	n := 0
	for _, v := range []string{w.At, w.Every, w.Cron} {
		if strings.TrimSpace(v) != "" {
			n++
		}
	}
	return n
}

// When is the time the flags ask for, nil when they name none. A wall time is
// read in zone, which zoneOf gives when it is needed and only then.
func (w WhenFlags) When(zoneOf func() (string, error)) (*client.ScheduleWhen, error) {
	switch w.given() {
	case 0:
		return nil, nil
	case 1:
	default:
		return nil, errors.New("say when with one of --at, --every or --cron, not more")
	}
	switch {
	case strings.TrimSpace(w.Cron) != "":
		rule := strings.TrimSpace(w.Cron)
		return &client.ScheduleWhen{Kind: client.Cron, Cron: &rule}, nil
	case strings.TrimSpace(w.Every) != "":
		sec, err := Length("every", w.Every)
		if err != nil {
			return nil, err
		}
		return &client.ScheduleWhen{Kind: client.Every, EverySeconds: &sec}, nil
	}
	at, err := atOf(w.At, zoneOf)
	if err != nil {
		return nil, err
	}
	return &client.ScheduleWhen{Kind: client.At, At: &at}, nil
}

func atOf(v string, zoneOf func() (string, error)) (time.Time, error) {
	v = strings.TrimSpace(v)
	if at, err := time.Parse(time.RFC3339, v); err == nil {
		return at, nil
	}
	if _, err := time.Parse(wallTime, v); err != nil {
		return time.Time{}, fmt.Errorf("--at is RFC 3339 or YYYY-MM-DD HH:MM, not %q", v)
	}
	zone, err := zoneOf()
	if err != nil {
		return time.Time{}, err
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, fmt.Errorf("the zone %q is not one this machine knows: %w", zone, err)
	}
	at, err := time.ParseInLocation(wallTime, v, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("--at is RFC 3339 or YYYY-MM-DD HH:MM, not %q", v)
	}
	return at, nil
}

// TeamZone is the team's zone, read from its schedule settings: what a wall
// time is read in when --tz is not given.
func TeamZone(ctx context.Context, api *client.ClientWithResponses, team uuid.UUID) (string, error) {
	s, err := cli.Result(api.GetScheduleSettingsWithResponse(ctx, team))
	if err != nil {
		return "", fmt.Errorf("read the team's zone: %w", err)
	}
	return s.Timezone, nil
}

// Target reads --target: own, chat:<session>, channel:<channel>:<ref> or
// direct:<channel>, where a channel is a name or an id. Empty is own, the
// default, and nil is sent.
func Target(
	ctx context.Context, api *client.ClientWithResponses, team uuid.UUID, v string,
) (*client.ScheduleTargetPick, error) {
	v = strings.TrimSpace(v)
	kind, rest, _ := strings.Cut(v, ":")
	bad := fmt.Errorf("--target is own, chat:<session>, channel:<channel>:<ref> or direct:<channel>, not %q; "+
		"`oa schedules places` lists them", v)
	switch kind {
	case "", "own":
		if rest != "" {
			return nil, bad
		}
		if kind == "" {
			return nil, nil
		}
		return &client.ScheduleTargetPick{Kind: client.ScheduleTargetPickKindOwn}, nil
	case "chat":
		id, err := uuid.Parse(rest)
		if err != nil {
			return nil, bad
		}
		return &client.ScheduleTargetPick{Kind: client.ScheduleTargetPickKindChat, SessionID: &id}, nil
	case "channel":
		channel, ref, ok := strings.Cut(rest, ":")
		if !ok || channel == "" || ref == "" {
			return nil, bad
		}
		id, err := cli.ResolveChannel(ctx, api, team, channel)
		if err != nil {
			return nil, err
		}
		return &client.ScheduleTargetPick{Kind: client.ScheduleTargetPickKindChannel, ChannelID: &id, Ref: &ref}, nil
	case "direct":
		if rest == "" || strings.Contains(rest, ":") {
			return nil, bad
		}
		id, err := cli.ResolveChannel(ctx, api, team, rest)
		if err != nil {
			return nil, err
		}
		return &client.ScheduleTargetPick{Kind: client.ScheduleTargetPickKindDirect, ChannelID: &id}, nil
	}
	return nil, bad
}

// PickSays is a place as --target takes it, for `places` to print.
func PickSays(p client.ScheduleTargetPick) string {
	switch p.Kind {
	case client.ScheduleTargetPickKindChat:
		if p.SessionID != nil {
			return "chat:" + p.SessionID.String()
		}
	case client.ScheduleTargetPickKindChannel:
		if p.ChannelID != nil && p.Ref != nil {
			return "channel:" + p.ChannelID.String() + ":" + *p.Ref
		}
	case client.ScheduleTargetPickKindDirect:
		if p.ChannelID != nil {
			return "direct:" + p.ChannelID.String()
		}
	case client.ScheduleTargetPickKindOwn:
		return "own"
	}
	return ""
}

// AgentFlags are --agent and --hub: which agent runs it.
type AgentFlags struct {
	Agent string
	Hub   bool
}

func (a *AgentFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&a.Agent, "agent", "", "the agent that runs it, by name or id; the hub unless given")
	cmd.Flags().BoolVar(&a.Hub, "hub", false, "the hub runs it")
	cmd.MarkFlagsMutuallyExclusive("agent", "hub")
}

// ID is the agent named, nil for the hub or when neither flag was given.
func (a AgentFlags) ID(ctx context.Context, api *client.ClientWithResponses, team uuid.UUID) (*uuid.UUID, error) {
	if strings.TrimSpace(a.Agent) == "" {
		return nil, nil
	}
	id, err := cli.ResolveAgent(ctx, api, team, a.Agent)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// ObjectiveFlags are --objective and --objective-file.
type ObjectiveFlags struct{ Text, File string }

func (o *ObjectiveFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.Text, "objective", "", "what each run is to do: its whole instruction")
	cmd.Flags().StringVar(&o.File, "objective-file", "", "read the objective from a file, or - for stdin")
	cmd.MarkFlagsMutuallyExclusive("objective", "objective-file")
}

// Read is the objective given, and whether one was.
func (o ObjectiveFlags) Read(stdin io.Reader) (string, bool, error) {
	text := o.Text
	switch {
	case o.File == "-":
		b, err := io.ReadAll(io.LimitReader(stdin, maxObjective*4+1))
		if err != nil {
			return "", false, fmt.Errorf("read the objective from stdin: %w", err)
		}
		text = string(b)
	case o.File != "":
		b, err := os.ReadFile(o.File)
		if err != nil {
			return "", false, fmt.Errorf("read the objective: %w", err)
		}
		text = string(b)
	case text == "":
		return "", false, nil
	}
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return "", false, errors.New("the objective is empty")
	case utf8.RuneCountInString(text) > maxObjective:
		return "", false, fmt.Errorf("the objective is %d characters; the brain takes %d at most",
			utf8.RuneCountInString(text), maxObjective)
	}
	return text, true, nil
}

// BindAgent adds --agent and --hub to a command, for watchers as for
// schedules.
func BindAgent(cmd *cobra.Command, a *AgentFlags) { a.bind(cmd) }

// BindObjective adds --objective and --objective-file to a command.
func BindObjective(cmd *cobra.Command, o *ObjectiveFlags) { o.bind(cmd) }
