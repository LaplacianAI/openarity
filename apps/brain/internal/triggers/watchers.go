package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
	"github.com/LaplacianAI/openarity/apps/brain/internal/triggers/watch"
)

// The sources a watcher watches: a page and a feed are polled; a webhook is
// called.
const (
	SourceWebPage = watch.WebPage
	SourceFeed    = watch.Feed
	SourceWebhook = "webhook"
)

// WaitingSecret is a webhook watcher an agent proposed, waiting for its maker
// to make its secret.
const WaitingSecret = "waiting_secret"

const (
	// defaultCooldown is how long a watcher waits after a run before the next
	// change starts another; a change in between is held, not lost.
	defaultCooldown = time.Hour
	maxCooldown     = 7 * 24 * time.Hour
	maxEvery        = 31 * 24 * time.Hour
	// waitingLife is how long a proposed webhook watcher waits for its secret.
	waitingLife = 7 * 24 * time.Hour
)

// WatcherInput is a new watcher. A zero Cooldown is an hour; a nil AgentID is
// the hub; a zero Target is its own conversation.
type WatcherInput struct {
	Name, Objective string
	Source          string
	URL, Selector   string
	Every           time.Duration
	Cooldown        *time.Duration
	AgentID         *uuid.UUID
	Target          Target
}

// WatcherPatch changes what it sets, all at once or none of it. An empty
// Selector clears it. Hub sets the agent back to the hub; Status pauses or
// resumes. What it does is the maker's alone to change, as it runs as them.
type WatcherPatch struct {
	Name, Objective *string
	Every, Cooldown *time.Duration
	Selector        *string
	AgentID         *uuid.UUID
	Hub             bool
	Status          *string
}

func (p WatcherPatch) changesWhatItDoes() bool {
	return p.Name != nil || p.Objective != nil || p.Every != nil || p.Cooldown != nil ||
		p.Selector != nil || p.AgentID != nil || p.Hub
}

// A WatcherView is a watcher as it is shown. Secret is a webhook watcher's,
// only in the answer that made or rolled it.
type WatcherView struct {
	db.Watcher
	SessionID        uuid.UUID
	SessionChannelID *uuid.UUID
	URL              string
	LastOutcome      string
	LastCheckedAt    *time.Time
	Secret           string
}

// CreateWatcher saves a new watcher and its conversation. A page or feed
// has its first check due now: that check is its baseline, and starts
// nothing (D12). Made by a person it is active at once; proposed by an
// agent's tool, it waits for the person's next message to confirm it (D10).
// A webhook watcher made by a person is active with its secret, shown once;
// proposed by a tool it waits for its maker to make the secret on the
// dashboard, which is the confirmation, and no secret passes through a chat.
func (s *Service) CreateWatcher(ctx context.Context, who Who, origin Origin, in WatcherInput) (WatcherView, error) {
	name, objective, err := named("watcher", in.Name, in.Objective)
	if err != nil {
		return WatcherView{}, err
	}
	l, err := s.limits(ctx, who.Team)
	if err != nil {
		return WatcherView{}, err
	}
	var how poll
	if in.Source == SourceWebhook {
		how.cooldown, err = hooked(in)
	} else {
		how, err = polled(in, l)
	}
	if err != nil {
		return WatcherView{}, err
	}
	if in.Target.Kind != "" && in.Target.Kind != TargetOwn {
		if err := s.store.InTx(ctx, func(q *db.Queries) error { return mayMake(ctx, q, who, in.AgentID, l) }); err != nil {
			return WatcherView{}, err
		}
	}
	at, err := s.place(ctx, who, in.Target)
	if err != nil {
		return WatcherView{}, err
	}

	now := s.now()
	id := uuid.New()
	var hook, secret string
	if in.Source == SourceWebhook {
		hook = newHookID()
		if !origin.viaTool() {
			secret = newSecret()
		}
	}
	err = s.store.InTx(ctx, func(q *db.Queries) error {
		if err := mayMake(ctx, q, who, in.AgentID, l); err != nil {
			return err
		}
		row := db.CreateWatcherParams{
			ID: id, TeamID: who.Team, CreatedBy: who.User, AgentID: in.AgentID, Hub: in.AgentID == nil,
			Name: name, Objective: objective, Source: in.Source, Selector: how.selector,
			CooldownSeconds: seconds(how.cooldown), Status: Active, Target: TargetOwn,
		}
		if in.Source == SourceWebhook {
			row.HookID = &hook
		} else {
			target, err := q.UpsertWatchTarget(ctx, db.UpsertWatchTargetParams{
				ID: uuid.New(), Kind: in.Source, Url: how.url, Host: how.host, NextCheckAt: now,
			})
			if err != nil {
				return fmt.Errorf("save the address it watches: %w", err)
			}
			row.TargetID, row.EverySeconds, row.NextCheckAt = &target.ID, ptr(seconds(how.every)), &now
		}
		if origin.viaTool() {
			row.Status, row.NextCheckAt = Proposed, nil
			row.ProposedRun, row.ProposedIn = &origin.Run, &origin.Session
			if in.Source == SourceWebhook {
				row.Status = WaitingSecret
			}
		}
		session, err := sessionAt(ctx, q, who, at, id)
		if err != nil {
			return err
		}
		if at != nil {
			row.Target, row.TargetLabel = at.target, at.label
		}
		if _, err := q.CreateWatcher(ctx, row); err != nil {
			return watcherNameTaken(err, name)
		}
		session.WatcherID = &id
		if _, err := q.CreateTriggerSession(ctx, session); err != nil {
			return fmt.Errorf("start the watcher's conversation: %w", err)
		}
		if secret == "" {
			return nil
		}
		// Written in the transaction that saves the row, so none is made
		// without its secret kept; one written for a row that then fails to
		// commit is at a path nothing reads.
		return s.putSecret(ctx, &db.Watcher{ID: id, TeamID: who.Team}, secret)
	})
	if err != nil {
		return WatcherView{}, err
	}
	v, err := s.watcherView(ctx, who, id)
	if err != nil {
		return WatcherView{}, err
	}
	v.Secret = secret
	return v, nil
}

// sessionAt is the trigger session a schedule or watcher is made with: where
// place put it, or a conversation of its own on the dashboard.
func sessionAt(ctx context.Context, q *db.Queries, who Who, at *placed, id uuid.UUID) (db.CreateTriggerSessionParams, error) {
	session := db.CreateTriggerSessionParams{
		TeamID: who.Team, Kind: string(gateway.SessionDirect), UserID: &who.User,
	}
	if at != nil {
		session.ChannelID, session.ProviderRef, session.Kind, session.UserID = at.channel, at.ref, at.kind, at.user
		return session, nil
	}
	ch, err := q.DashboardChannel(ctx, who.Team)
	if err != nil {
		return session, fmt.Errorf("read the dashboard channel: %w", err)
	}
	session.ChannelID, session.ProviderRef = ch.ID, "trigger:"+id.String()
	return session, nil
}

// poll is a polling watcher's address and timing, checked.
type poll struct {
	url, host       string
	selector        *string
	every, cooldown time.Duration
}

func polled(in WatcherInput, l Limits) (poll, error) {
	switch in.Source {
	case SourceWebPage, SourceFeed:
	default:
		return poll{}, refuse(Invalid, "a watcher's source is web_page, feed or webhook")
	}
	u, host, err := watch.Normalize(in.URL)
	if err != nil {
		return poll{}, refuse(Invalid, "%s", err.Error())
	}
	p := poll{url: u, host: host, cooldown: defaultCooldown}
	if in.Selector != "" {
		if in.Source != SourceWebPage {
			return poll{}, refuse(Invalid, "only a web_page watcher takes a selector")
		}
		if err := watch.CompileSelector(in.Selector); err != nil {
			return poll{}, refuse(Invalid, "%s", err.Error())
		}
		p.selector = &in.Selector
	}
	if p.every, err = everyOf(in.Every, l); err != nil {
		return poll{}, err
	}
	if in.Cooldown != nil {
		if p.cooldown, err = cooldownOf(*in.Cooldown); err != nil {
			return poll{}, err
		}
	}
	return p, nil
}

func everyOf(every time.Duration, l Limits) (time.Duration, error) {
	switch {
	case every <= 0:
		return 0, refuse(Invalid, "a watcher needs every: how often to look")
	case every%time.Minute != 0:
		return 0, refuse(Invalid, "every is in whole minutes")
	case every < l.MinInterval:
		return 0, refuse(Invalid, "this team looks at most every %s; ask an admin to lower its minimum interval", l.MinInterval)
	case every > maxEvery:
		return 0, refuse(Invalid, "every is at most 31 days")
	}
	return every, nil
}

func cooldownOf(c time.Duration) (time.Duration, error) {
	if c < 0 || c > maxCooldown || c%time.Second != 0 {
		return 0, refuse(Invalid, "cooldown is whole seconds, from 0 to 7 days")
	}
	return c, nil
}

func seconds(d time.Duration) int32 { return int32(d / time.Second) } //nolint:gosec // bounded to 31 days

// Watcher is one the caller may see.
func (s *Service) Watcher(ctx context.Context, who Who, ref Ref) (WatcherView, error) {
	id, err := s.resolveWatcher(ctx, who, ref)
	if err != nil {
		return WatcherView{}, err
	}
	return s.watcherView(ctx, who, id)
}

// ListWatchers is the caller's own, newest first, or everyone's for one who
// may manage them. No statuses is every one not deleted.
func (s *Service) ListWatchers(ctx context.Context, who Who, everyone bool, statuses []string,
	after *time.Time, afterID *uuid.UUID, limit int32,
) ([]WatcherView, error) {
	if everyone && !who.Manage {
		return nil, refuse(Forbidden, "seeing everyone's watchers takes schedule:manage")
	}
	if statuses == nil {
		statuses = []string{}
	}
	rows, err := s.store.ListWatcherViews(ctx, db.ListWatcherViewsParams{
		TeamID: who.Team, Everyone: everyone, Viewer: who.User, Statuses: statuses,
		AfterCreated: after, AfterID: afterID, PageSize: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list the watchers: %w", err)
	}
	out := make([]WatcherView, len(rows))
	for i, r := range rows {
		out[i] = WatcherView{
			Watcher: r.Watcher, SessionID: r.SessionID, SessionChannelID: r.SessionChannelID,
			URL: r.Url, LastOutcome: r.LastOutcome, LastCheckedAt: r.LastCheckedAt,
		}
	}
	return out, nil
}

// Checks is the history, newest first, of a watcher the caller may see.
func (s *Service) Checks(ctx context.Context, who Who, ref Ref, before *time.Time, limit int32) ([]db.WatcherCheck, error) {
	id, err := s.resolveWatcher(ctx, who, ref)
	if err != nil {
		return nil, err
	}
	if _, err := s.watcherView(ctx, who, id); err != nil {
		return nil, err
	}
	rows, err := s.store.ListWatcherChecks(ctx, db.ListWatcherChecksParams{
		WatcherID: id, TeamID: who.Team, Before: before, PageSize: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list the watcher's checks: %w", err)
	}
	return rows, nil
}

// UpdateWatcher changes what the patch sets, in one transaction. The address
// and the conversation it answers in never move: delete it and make another.
func (s *Service) UpdateWatcher(ctx context.Context, who Who, ref Ref, p WatcherPatch) (WatcherView, error) {
	if p.Status != nil && *p.Status != Active && *p.Status != Paused {
		return WatcherView{}, refuse(Invalid, "status is active or paused")
	}
	l, err := s.limits(ctx, who.Team)
	if err != nil {
		return WatcherView{}, err
	}
	now := s.now()
	id, err := s.changeWatcher(ctx, who, ref, func(q *db.Queries, row *db.Watcher) error {
		if p.changesWhatItDoes() && !madeBy(who, row.CreatedBy) {
			return refuse(Forbidden, "only the person who made this watcher may change what it does or check it now")
		}
		if err := patchWatcher(ctx, q, row, p, l); err != nil {
			return err
		}
		switch {
		case p.Status == nil || *p.Status == row.Status:
			return nil
		case *p.Status == Paused:
			return pauseWatcher(row)
		default:
			return resumeWatcher(ctx, q, row, now)
		}
	})
	if err != nil {
		return WatcherView{}, err
	}
	return s.watcherView(ctx, who, id)
}

func patchWatcher(ctx context.Context, q *db.Queries, row *db.Watcher, p WatcherPatch, l Limits) error {
	if p.Name != nil || p.Objective != nil {
		name, objective := row.Name, row.Objective
		if p.Name != nil {
			name = *p.Name
		}
		if p.Objective != nil {
			objective = *p.Objective
		}
		var err error
		if row.Name, row.Objective, err = named("watcher", name, objective); err != nil {
			return err
		}
	}
	switch {
	case p.Hub:
		row.AgentID, row.Hub = nil, true
	case p.AgentID != nil:
		if err := agentInTeam(ctx, q, row.TeamID, p.AgentID); err != nil {
			return err
		}
		row.AgentID, row.Hub = p.AgentID, false
	}
	if p.Every != nil {
		if row.Source == SourceWebhook {
			return refuse(Invalid, "a webhook watcher has no every: it runs when its URL is called")
		}
		every, err := everyOf(*p.Every, l)
		if err != nil {
			return err
		}
		row.EverySeconds = ptr(seconds(every))
	}
	if p.Cooldown != nil {
		c, err := cooldownOf(*p.Cooldown)
		if err != nil {
			return err
		}
		row.CooldownSeconds = seconds(c)
	}
	if p.Selector != nil {
		if row.Source != SourceWebPage {
			return refuse(Invalid, "only a web_page watcher takes a selector")
		}
		if *p.Selector == "" {
			row.Selector = nil
		} else {
			if err := watch.CompileSelector(*p.Selector); err != nil {
				return refuse(Invalid, "%s", err.Error())
			}
			row.Selector = p.Selector
		}
		// What it reads changed, so what it saw no longer compares: the next
		// check is a new baseline, not a change.
		row.State = []byte("{}")
	}
	return nil
}

func pauseWatcher(row *db.Watcher) error {
	if row.Status != Active {
		return refuse(Conflict, "only an active watcher can be paused; this one is %s", row.Status)
	}
	row.Status, row.PausedReason, row.NextCheckAt = Paused, ptr(ByPerson), nil
	return nil
}

// resumeWatcher starts a paused watcher again with a check now, as long as its
// maker may still run and post where it answers, and its agent is there.
func resumeWatcher(ctx context.Context, q *db.Queries, row *db.Watcher, now time.Time) error {
	if row.Status != Paused {
		return refuse(Conflict, "only a paused watcher can be resumed; this one is %s", row.Status)
	}
	if row.CreatedBy == nil {
		return refuse(Conflict, "the person who made this watcher has left; an admin can take it over first")
	}
	if err := mayRun(ctx, q, row.TeamID, *row.CreatedBy, Conflict,
		"the person who made this watcher may no longer start runs in this team; an admin can take it over first"); err != nil {
		return err
	}
	if may, err := makerMayPost(ctx, q, watcherOf(row)); err != nil {
		return err
	} else if !may {
		return refuse(Conflict, "the person who made this watcher may no longer post where a group reads it "+
			"(channel:write); an admin can take it over first")
	}
	if !row.Hub && row.AgentID == nil {
		return refuse(Conflict, "the agent this watcher ran was deleted; choose another, or the hub, first")
	}
	row.Status, row.PausedReason, row.NextCheckAt, row.ConsecutiveFailures = Active, nil, &now, 0
	if row.Source == SourceWebhook {
		// What it held while paused is dropped: it starts again from what
		// is delivered next (§3.4).
		row.NextCheckAt = nil
		if err := q.LetGoOfHeldDeliveries(ctx, row.ID); err != nil {
			return fmt.Errorf("drop what the watcher held: %w", err)
		}
		return nil
	}
	// Others' fetches may have moved the target's validators past a change
	// it never saw while paused: its next look reads the address whole.
	if row.TargetID != nil {
		if err := q.ForgetTargetValidators(ctx, *row.TargetID); err != nil {
			return fmt.Errorf("have the watcher's next look read the address whole: %w", err)
		}
	}
	return checkSoon(ctx, q, row, now)
}

// checkSoon has a watcher's target fetched by at, for a watcher due then.
func checkSoon(ctx context.Context, q *db.Queries, row *db.Watcher, at time.Time) error {
	if row.TargetID == nil {
		return nil
	}
	if err := q.PullTargetCheck(ctx, db.PullTargetCheckParams{ID: *row.TargetID, At: at}); err != nil {
		return fmt.Errorf("bring the watcher's check forward: %w", err)
	}
	return nil
}

// CheckNow has an active watcher look now rather than at its next time: a
// change it finds runs as any would, past its cooldown.
func (s *Service) CheckNow(ctx context.Context, who Who, ref Ref) (WatcherView, error) {
	now := s.now()
	id, err := s.changeWatcher(ctx, who, ref, func(q *db.Queries, row *db.Watcher) error {
		if !madeBy(who, row.CreatedBy) {
			return refuse(Forbidden, "only the person who made this watcher may change what it does or check it now")
		}
		if row.Status != Active {
			return refuse(Conflict, "only an active watcher can be checked; this one is %s", row.Status)
		}
		if row.Source == SourceWebhook {
			return refuse(Invalid, "a webhook watcher runs when its URL is called; there is nothing for it to look at")
		}
		row.NextCheckAt = &now
		return checkSoon(ctx, q, row, now)
	})
	if err != nil {
		return WatcherView{}, err
	}
	return s.watcherView(ctx, who, id)
}

// ConfirmWatcher makes a proposal active, with its first check now. From an
// agent's tool, only a run started by a later message from the same person,
// in the conversation it was proposed in, may.
func (s *Service) ConfirmWatcher(ctx context.Context, who Who, origin Origin, ref Ref) (WatcherView, error) {
	now := s.now()
	id, err := s.changeWatcher(ctx, who, ref, func(q *db.Queries, row *db.Watcher) error {
		if row.Status != Proposed {
			return refuse(Conflict, "this watcher is not waiting to be confirmed; it is %s", row.Status)
		}
		if !madeBy(who, row.CreatedBy) {
			return refuse(Forbidden, "only the person it was proposed to may confirm it")
		}
		if now.Sub(row.CreatedAt) > proposalLife {
			return refuse(Conflict, "the proposal waited more than an hour and lapsed; ask for it again")
		}
		if origin.viaTool() {
			if err := laterMessage(ctx, q, proposal{
				run: row.ProposedRun, in: row.ProposedIn, maker: row.CreatedBy, at: row.CreatedAt, noun: "watcher",
			}, origin); err != nil {
				return err
			}
		}
		row.Status, row.NextCheckAt = Active, &now
		return checkSoon(ctx, q, row, now)
	})
	if err != nil {
		return WatcherView{}, err
	}
	return s.watcherView(ctx, who, id)
}

// DeleteWatcher deletes a watcher as a schedule is: marked so, its
// conversation closed and kept with its runs. It lets go of its address,
// which the sweep deletes once nobody watches it. A webhook watcher drops
// what it held, and its secret is destroyed, so its URL answers 404 from
// then on whatever the secret store says.
func (s *Service) DeleteWatcher(ctx context.Context, who Who, ref Ref) error {
	now := s.now()
	var hooked *db.Watcher
	_, err := s.changeWatcher(ctx, who, ref, func(q *db.Queries, row *db.Watcher) error {
		row.Status, row.PausedReason, row.NextCheckAt, row.DeletedAt = Deleted, nil, nil, &now
		row.TargetID = nil
		if err := q.CloseTriggerSession(ctx, db.CloseTriggerSessionParams{WatcherID: &row.ID}); err != nil {
			return fmt.Errorf("close the watcher's conversation: %w", err)
		}
		if row.Source != SourceWebhook {
			return nil
		}
		hooked = row
		if err := q.LetGoOfHeldDeliveries(ctx, row.ID); err != nil {
			return fmt.Errorf("drop what the watcher held: %w", err)
		}
		return nil
	})
	if err != nil || hooked == nil {
		return err
	}
	if err := s.secrets.Delete(ctx, secretPath(hooked)); err != nil && !errors.Is(err, secrets.ErrNotFound) {
		s.logger.WarnContext(ctx, "A deleted webhook watcher's secret was not destroyed; its URL answers 404 all the same",
			"watcher_id", hooked.ID, "error", err)
	}
	return nil
}

// TakeOverWatcher gives a paused watcher whose maker cannot resume it to the
// admin asking, as with a schedule. It stays paused until they resume it. A
// webhook watcher gets a new secret, shown once: the one its old maker held
// would otherwise sign what now runs as the admin.
func (s *Service) TakeOverWatcher(ctx context.Context, who Who, ref Ref) (WatcherView, error) {
	if !who.Manage {
		return WatcherView{}, refuse(Forbidden, "taking over a watcher takes schedule:manage")
	}
	var secret string
	id, err := s.changeWatcher(ctx, who, ref, func(q *db.Queries, row *db.Watcher) error {
		t := watcherOf(row)
		stuck, err := makerStuck(ctx, q, t)
		if err != nil {
			return err
		}
		if !stuck {
			return refuse(Conflict, "only a paused watcher whose maker left, or may no longer run or post where it "+
				"answers, can be taken over")
		}
		group, err := postsInGroup(ctx, q, t)
		if err != nil {
			return err
		}
		if row.Target == TargetDirect || (row.Target == TargetChat && !group) {
			return refuse(Conflict, "this watcher answers in its maker's direct messages or their own thread, "+
				"so it cannot be handed on; delete it and make another")
		}
		if err := mayRun(ctx, q, row.TeamID, who.User, Forbidden,
			"you may not start runs in this team, so a watcher taken over by you could not run"); err != nil {
			return err
		}
		if group && !who.ChannelWrite {
			return refuse(Forbidden, "this watcher posts where a group reads it, which takes channel:write")
		}
		row.CreatedBy = &who.User
		if err := q.SetWatcherSessionUser(ctx, db.SetWatcherSessionUserParams{UserID: who.User, WatcherID: row.ID}); err != nil {
			return fmt.Errorf("hand over the watcher's conversation: %w", err)
		}
		if row.Source != SourceWebhook {
			return nil
		}
		// Last, so nothing after it can fail: should the commit, the old
		// maker's secret has stopped working all the same.
		secret = newSecret()
		return s.putSecret(ctx, row, secret)
	})
	if err != nil {
		return WatcherView{}, err
	}
	v, err := s.watcherView(ctx, who, id)
	if err != nil {
		return WatcherView{}, err
	}
	v.Secret = secret
	return v, nil
}

// WatcherNews is what the caller has not yet seen of their own watchers.
func (s *Service) WatcherNews(ctx context.Context, who Who, limit int32) ([]db.ListWatcherNewsRow, error) {
	rows, err := s.store.ListWatcherNews(ctx, db.ListWatcherNewsParams{TeamID: who.Team, Maker: who.User, PageSize: limit})
	if err != nil {
		return nil, fmt.Errorf("list the news of your watchers: %w", err)
	}
	return rows, nil
}

// WatcherSeen marks what a watcher of the caller's did as seen.
func (s *Service) WatcherSeen(ctx context.Context, who Who, watcher uuid.UUID) error {
	if _, err := s.store.MarkWatcherSeen(ctx, db.MarkWatcherSeenParams{WatcherID: watcher, TeamID: who.Team, Maker: who.User}); err != nil {
		return fmt.Errorf("mark the watcher seen: %w", err)
	}
	return nil
}

func (s *Service) resolveWatcher(ctx context.Context, who Who, ref Ref) (uuid.UUID, error) {
	if ref.ID != uuid.Nil {
		return ref.ID, nil
	}
	id, err := s.store.WatcherIDByName(ctx, db.WatcherIDByNameParams{TeamID: who.Team, Maker: who.User, Name: ref.Name})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, refuse(NotFound, "you have no watcher named %q", ref.Name)
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("find the watcher %q: %w", ref.Name, err)
	}
	return id, nil
}

// changeWatcher locks a watcher the caller may act on, lets fn change it, and
// saves it whole, in one transaction.
func (s *Service) changeWatcher(ctx context.Context, who Who, ref Ref, fn func(*db.Queries, *db.Watcher) error) (uuid.UUID, error) {
	id, err := s.resolveWatcher(ctx, who, ref)
	if err != nil {
		return uuid.Nil, err
	}
	err = s.store.InTx(ctx, func(q *db.Queries) error {
		row, err := q.LockWatcher(ctx, db.LockWatcherParams{ID: id, TeamID: who.Team})
		if errors.Is(err, pgx.ErrNoRows) {
			return refuse(NotFound, "no such watcher")
		}
		if err != nil {
			return fmt.Errorf("lock the watcher: %w", err)
		}
		if !who.Manage && !madeBy(who, row.CreatedBy) {
			return refuse(NotFound, "no such watcher")
		}
		if err := fn(q, &row); err != nil {
			return err
		}
		if _, err := q.SaveWatcher(ctx, saveWatcherOf(row)); err != nil {
			return watcherNameTaken(err, row.Name)
		}
		return nil
	})
	return id, err
}

func (s *Service) watcherView(ctx context.Context, who Who, id uuid.UUID) (WatcherView, error) {
	r, err := s.store.GetWatcherView(ctx, db.GetWatcherViewParams{ID: id, TeamID: who.Team})
	if errors.Is(err, pgx.ErrNoRows) {
		return WatcherView{}, refuse(NotFound, "no such watcher")
	}
	if err != nil {
		return WatcherView{}, fmt.Errorf("read the watcher: %w", err)
	}
	if !who.Manage && !madeBy(who, r.Watcher.CreatedBy) {
		return WatcherView{}, refuse(NotFound, "no such watcher")
	}
	return WatcherView{
		Watcher: r.Watcher, SessionID: r.SessionID, SessionChannelID: r.SessionChannelID,
		URL: r.Url, LastOutcome: r.LastOutcome, LastCheckedAt: r.LastCheckedAt,
	}, nil
}

// madeBy is a trigger the caller made.
func madeBy(who Who, maker *uuid.UUID) bool {
	return maker != nil && *maker == who.User
}

func watcherNameTaken(err error, name string) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "watchers_name_key" {
		return refuse(Conflict, "you already have a watcher named %q", name)
	}
	return fmt.Errorf("save the watcher: %w", err)
}

func saveWatcherOf(r db.Watcher) db.SaveWatcherParams {
	state := r.State
	if len(state) == 0 {
		state = []byte("{}")
	}
	return db.SaveWatcherParams{
		ID: r.ID, CreatedBy: r.CreatedBy, AgentID: r.AgentID, Hub: r.Hub, Name: r.Name, Objective: r.Objective,
		TargetID: r.TargetID, Selector: r.Selector, EverySeconds: r.EverySeconds, CooldownSeconds: r.CooldownSeconds,
		State: state, Status: r.Status, PausedReason: r.PausedReason, DeletedAt: r.DeletedAt,
		NextCheckAt: r.NextCheckAt, LastRunAt: r.LastRunAt, ConsecutiveFailures: r.ConsecutiveFailures,
	}
}

// stateOf is what a watcher saw last, or nothing for one never checked.
func stateOf(row db.Watcher) (watch.State, error) {
	var st watch.State
	if len(row.State) == 0 {
		return st, nil
	}
	if err := json.Unmarshal(row.State, &st); err != nil {
		return st, fmt.Errorf("read watcher %s's state: %w", row.ID, err)
	}
	return st, nil
}
