package trigger

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
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

// How a time, a length, a place, an agent and an objective are read from
// flags. The brain checks each again; reading them here is so a mistake is
// said before anything is sent, in the words of the flag that made it.

// MaxObjective is the brain's own cap on an objective, in characters.
const MaxObjective = 4000

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

// OptLength is Length for a flag that may be left out: nil when it was.
func OptLength(flag, v string) (*int64, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	sec, err := Length(flag, v)
	if err != nil {
		return nil, err
	}
	return &sec, nil
}

// maxCooldown is the brain's longest cooldown, 7 days.
const maxCooldown = 7 * 24 * 60 * 60

// OptCooldown reads --cooldown: a length, 0 for no hold at all, or nil
// when it was left out and the brain's hour holds.
func OptCooldown(v string) (*int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	if d, err := lengthOf(v); err == nil && d == 0 {
		var none int64
		return &none, nil
	}
	sec, err := Length("cooldown", v)
	if err != nil {
		return nil, err
	}
	if sec > maxCooldown {
		return nil, fmt.Errorf("--cooldown is at most %s, not %q", LengthSays(maxCooldown), v)
	}
	return &sec, nil
}

func lengthOf(v string) (time.Duration, error) {
	for suffix, unit := range map[string]time.Duration{"d": 24 * time.Hour, "w": 7 * 24 * time.Hour} {
		if n, ok := strings.CutSuffix(v, suffix); ok {
			k, err := strconv.ParseInt(n, 10, 64)
			if err != nil {
				return 0, err
			}
			// More days than a duration holds would wrap round.
			if k < 0 || k > math.MaxInt64/int64(unit) {
				return 0, errors.New("out of range")
			}
			return time.Duration(k) * unit, nil
		}
	}
	return time.ParseDuration(v)
}

// WhenFlags are --at, --every and --cron.
type WhenFlags struct{ At, Every, Cron string }

// Bind adds the three flags to cmd.
func (w *WhenFlags) Bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&w.At, "at", "", "once, at this time: RFC 3339, or YYYY-MM-DD HH:MM read in --tz")
	cmd.Flags().StringVar(&w.Every, "every", "", "repeatedly, this often: 30m, 1h, 1d or 1w")
	cmd.Flags().StringVar(&w.Cron, "cron", "", "on a cron rule, five fields or @daily, read in --tz")
}

// Given is how many of the three were set.
func (w WhenFlags) Given() int {
	n := 0
	for _, v := range []string{w.At, w.Every, w.Cron} {
		if strings.TrimSpace(v) != "" {
			n++
		}
	}
	return n
}

// Check is what When would refuse that needs no zone to know, so it can be
// said before anything is asked of the brain.
func (w WhenFlags) Check() error {
	if w.Given() > 1 {
		return errors.New("say when with one of --at, --every or --cron, not more")
	}
	if strings.TrimSpace(w.Every) != "" {
		if _, err := Length("every", w.Every); err != nil {
			return err
		}
	}
	if v := strings.TrimSpace(w.At); v != "" {
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			if _, err := naiveWall(v); err != nil {
				return err
			}
		}
	}
	return nil
}

// When is the time the flags ask for, nil when they name none. A wall time is
// read in zone, which zoneOf gives when it is needed and only then.
func (w WhenFlags) When(zoneOf func() (string, error)) (*client.ScheduleWhen, error) {
	if err := w.Check(); err != nil {
		return nil, err
	}
	switch {
	case w.Given() == 0:
		return nil, nil
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

// naiveWall reads a wall time with no zone yet, to check its form.
func naiveWall(v string) (time.Time, error) {
	at, err := time.Parse(wallTime, v)
	if err == nil {
		return at, nil
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05"} {
		if _, err := time.Parse(layout, v); err == nil {
			return time.Time{}, fmt.Errorf("--at %q has no offset: add one, such as Z or +05:30, "+
				"or write it YYYY-MM-DD HH:MM to read it in --tz", v)
		}
	}
	return time.Time{}, fmt.Errorf("--at is RFC 3339 or YYYY-MM-DD HH:MM, not %q", v)
}

func atOf(v string, zoneOf func() (string, error)) (time.Time, error) {
	v = strings.TrimSpace(v)
	if at, err := time.Parse(time.RFC3339, v); err == nil {
		return at, nil
	}
	naive, err := naiveWall(v)
	if err != nil {
		return time.Time{}, err
	}
	zone, err := zoneOf()
	if err != nil {
		return time.Time{}, err
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, fmt.Errorf("the zone %q is not one this machine knows: %w", zone, err)
	}
	at := time.Date(naive.Year(), naive.Month(), naive.Day(), naive.Hour(), naive.Minute(), 0, 0, loc)
	// A wall time the clocks skip is moved to another hour: refuse it rather
	// than run at a time nobody asked for.
	if at.Format(wallTime) != naive.Format(wallTime) {
		return time.Time{}, fmt.Errorf("--at %q does not happen in %s: the clocks skip it", v, zone)
	}
	return at, nil
}

// teamZone is the team's zone, read from its schedule settings: what a wall
// time is read in when --tz is not given.
func teamZone(ctx context.Context, api *client.ClientWithResponses, team uuid.UUID) (string, error) {
	s, err := cli.Result(api.GetScheduleSettingsWithResponse(ctx, team))
	if err != nil {
		return "", fmt.Errorf("read the team's zone: %w", err)
	}
	return s.Timezone, nil
}

// ZoneOr is what a wall time is read in: the zone given, or the team's.
func ZoneOr(ctx context.Context, api *client.ClientWithResponses, team uuid.UUID, zone string) func() (string, error) {
	return func() (string, error) {
		if zone != "" {
			return zone, nil
		}
		return teamZone(ctx, api, team)
	}
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

// PickSays is a place as --target takes it, for `places` to print. The ref
// comes from the platform, so it is made safe like any text it sent.
func PickSays(p client.ScheduleTargetPick) string {
	switch p.Kind {
	case client.ScheduleTargetPickKindChat:
		if p.SessionID != nil {
			return "chat:" + p.SessionID.String()
		}
	case client.ScheduleTargetPickKindChannel:
		if p.ChannelID != nil && p.Ref != nil {
			return "channel:" + p.ChannelID.String() + ":" + cli.Safe(*p.Ref)
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

// Bind adds --agent and --hub to cmd, as mutually exclusive.
func (a *AgentFlags) Bind(cmd *cobra.Command) {
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

// Bind adds --objective and --objective-file to cmd, as mutually exclusive.
func (o *ObjectiveFlags) Bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.Text, "objective", "", "what each run is to do: its whole instruction")
	cmd.Flags().StringVar(&o.File, "objective-file", "", "read the objective from a file, or - for stdin")
	cmd.MarkFlagsMutuallyExclusive("objective", "objective-file")
}

// readCap is the most read of a file or stdin: four bytes a character is
// the most UTF-8 takes, and one more says there was more.
const readCap = MaxObjective*utf8.UTFMax + 1

// Read is the objective given to cmd, and whether one was. One given empty
// is refused, not taken as left out.
func (o ObjectiveFlags) Read(cmd *cobra.Command) (string, bool, error) {
	var (
		text = o.Text
		b    []byte
		err  error
	)
	switch {
	case o.File == "-":
		if b, err = io.ReadAll(io.LimitReader(cmd.InOrStdin(), readCap)); err != nil {
			return "", false, fmt.Errorf("read the objective from stdin: %w", err)
		}
		text = string(b)
	case o.File != "":
		if b, err = readFile(o.File); err != nil {
			return "", false, fmt.Errorf("read the objective: %w", err)
		}
		text = string(b)
	case !cmd.Flags().Changed("objective"):
		return "", false, nil
	}
	text = strings.TrimSpace(text)
	switch {
	case len(b) >= readCap:
		return "", false, fmt.Errorf("the objective is over %d characters; the brain takes %d at most",
			MaxObjective, MaxObjective)
	case text == "":
		return "", false, errors.New("the objective is empty")
	case utf8.RuneCountInString(text) > MaxObjective:
		return "", false, fmt.Errorf("the objective is %d characters; the brain takes %d at most",
			utf8.RuneCountInString(text), MaxObjective)
	}
	return text, true, nil
}

// readFile is at most readCap bytes of path, so /dev/zero cannot fill memory.
func readFile(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // the person names their own file
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, readCap))
}

// Changed is whether any of names was given to cmd.
func Changed(cmd *cobra.Command, names ...string) bool {
	for _, n := range names {
		if cmd.Flags().Changed(n) {
			return true
		}
	}
	return false
}
