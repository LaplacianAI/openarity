package triggers

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// platform is a fake of the gateway's lookups: Slack lists channels and opens
// direct conversations; nothing else does.
type platform struct {
	mu       sync.Mutex
	channels map[uuid.UUID][]gateway.PlatformChannel
	refused  map[uuid.UUID]string
	opened   []string
	openErr  error
	nameErr  error
}

func (*platform) OpensDirect(p string) bool   { return p == "slack" }
func (*platform) ListsChannels(p string) bool { return p == "slack" }
func (*platform) Listers() []string           { return []string{"slack"} }

func (f *platform) OpenDirect(_ context.Context, _, _ uuid.UUID, _, user string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, user)
	if f.openErr != nil {
		return "", f.openErr
	}
	return "D0" + user[1:], nil
}

func (f *platform) ChannelName(_ context.Context, _, _ uuid.UUID, _, ref string) (string, error) {
	if f.nameErr != nil {
		return "", f.nameErr
	}
	return "chan-" + strings.ToLower(ref), nil
}

func (f *platform) ListChannels(_ context.Context, _, ch uuid.UUID, _ string) ([]gateway.PlatformChannel, error) {
	if msg, ok := f.refused[ch]; ok {
		return nil, &gateway.Refusal{Msg: msg}
	}
	return f.channels[ch], nil
}

// chats is a world with a Slack and a Telegram channel and the fake platform.
type chats struct {
	*world
	slack, telegram uuid.UUID
	plat            *platform
}

func newChats(t *testing.T) *chats {
	t.Helper()
	w := newWorld(t)
	c := &chats{world: w, plat: &platform{channels: map[uuid.UUID][]gateway.PlatformChannel{}, refused: map[uuid.UUID]string{}}}
	w.scan(t, &c.slack, `INSERT INTO channels (team_id, provider, name) VALUES ($1, 'slack', 'Acme Slack') RETURNING id`, w.team)
	w.scan(t, &c.telegram, `INSERT INTO channels (team_id, provider, name) VALUES ($1, 'telegram', 'Ops bot') RETURNING id`, w.team)
	w.service.WithChats(c.plat)
	return c
}

// session opens a conversation on a channel, and has user say something in
// it at the given time, when user is set.
func (c *chats) session(t *testing.T, channel uuid.UUID, ref, kind string, user *uuid.UUID, said time.Time) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	var participant *uuid.UUID // only a direct conversation names its person
	if kind == "direct" {
		participant = user
	}
	c.scan(t, &id, `INSERT INTO sessions (team_id, channel_id, provider_ref, kind, user_id)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`, c.team, channel, ref, kind, participant)
	if user != nil {
		c.exec(t, `INSERT INTO messages (channel_id, user_id, external_id, text, session_id, received_at)
			VALUES ($1, $2, $3, 'summarise the release notes please', $4, $5)`, channel, *user, uuid.NewString(), id, said)
	}
	return id
}

func (c *chats) letIn(t *testing.T, channel uuid.UUID, ref string, user uuid.UUID) {
	t.Helper()
	c.exec(t, `INSERT INTO channel_senders (channel_id, sender_ref, user_id) VALUES ($1, $2, $3)`, channel, ref, user)
}

// The chats a person may pick are theirs, lately, on a chat: not another's,
// not an old one, not the dashboard's, not a schedule's own.
func TestTheChatsAPersonTalkedInLatelyArePickable(t *testing.T) {
	c := newChats(t)
	now := time.Now()
	c.clock = now
	mine := c.session(t, c.slack, "C0RELEASE:1700000000.000100", "thread", &c.maker, now.Add(-time.Hour))
	c.session(t, c.slack, "C0RELEASE:1700000000.000200", "thread", &c.other, now.Add(-time.Hour))
	c.session(t, c.slack, "D0OLD", "direct", &c.maker, now.Add(-100*24*time.Hour))
	c.make(t, hourly("own")) // its own conversation on the dashboard is not a chat

	page, err := c.service.Targets(t.Context(), c.who(), TargetQuery{Kind: TargetChat, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Target.SessionID != mine {
		t.Fatalf("chats = %+v, want only the maker's recent thread", page.Items)
	}
	it := page.Items[0]
	if it.Label != "A thread" || !strings.HasPrefix(it.Detail, "You: summarise") || it.ChannelName != "Acme Slack" || !it.Available {
		t.Errorf("item = %+v", it)
	}
	if page, _ := c.service.Targets(t.Context(), c.who(), TargetQuery{Q: "RELEASE NOTES", Limit: 50}); len(page.Items) != 1 {
		t.Errorf("a search for its words found %d, want 1", len(page.Items))
	}
	if page, _ := c.service.Targets(t.Context(), c.who(), TargetQuery{Q: "nothing like it", Limit: 50}); len(page.Items) != 0 {
		t.Errorf("a search for other words found %d, want none", len(page.Items))
	}
}

// Channels come from the platform's own list where it gives one, and groups
// the bot heard from elsewhere. One the caller cannot use is listed with why,
// and a workspace that refused is one problem, not a failure.
func TestChannelsAreListedAndSayWhyOneCannotBePicked(t *testing.T) {
	c := newChats(t)
	var refusing uuid.UUID
	c.scan(t, &refusing, `INSERT INTO channels (team_id, provider, name) VALUES ($1, 'slack', 'Other Slack') RETURNING id`, c.team)
	c.plat.channels[c.slack] = []gateway.PlatformChannel{
		{Ref: "C0RELEASE", Name: "releases", Member: true, Members: 14},
		{Ref: "G0SECRET1", Name: "secret", Private: true, Member: false, Members: 3},
	}
	c.plat.refused[refusing] = "the Slack app needs the channels:read and groups:read scopes"
	group := c.session(t, c.telegram, "-100123", "group", nil, time.Now())
	c.session(t, c.slack, "C0SLACKGROUP", "group", nil, time.Now()) // a lister's group comes from its list

	page, err := c.service.Targets(t.Context(), c.who(), TargetQuery{Kind: TargetChannel, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("channels = %+v, want two Slack channels and the Telegram group", page.Items)
	}
	for _, it := range page.Items {
		if it.Available || it.Reason != ReasonNeedsChannelWrite {
			t.Errorf("without channel:write %s is %v/%q, want listed with why", it.Label, it.Available, it.Reason)
		}
	}
	if len(page.Problems) != 1 || page.Problems[0].ChannelID != refusing || !strings.Contains(page.Problems[0].Text, "channels:read") {
		t.Errorf("problems = %+v, want the refusing workspace", page.Problems)
	}

	writer := c.who()
	writer.ChannelWrite = true
	page, _ = c.service.Targets(t.Context(), writer, TargetQuery{Kind: TargetChannel, Limit: 50})
	byLabel := map[string]TargetItem{}
	for _, it := range page.Items {
		byLabel[it.Label] = it
	}
	if it := byLabel["#releases"]; !it.Available || it.Detail != "Public · 14 members" || it.Target.Ref != "C0RELEASE" {
		t.Errorf("#releases = %+v", it)
	}
	if it := byLabel["#secret"]; it.Available || it.Reason != ReasonNotInChannel || it.Detail != "Private · 3 members" {
		t.Errorf("#secret = %+v, want listed, not in it", it)
	}
	if it := byLabel["A group"]; !it.Available || it.Target.SessionID != group || it.Provider != "telegram" {
		t.Errorf("the Telegram group = %+v", it)
	}
}

// Direct conversations are offered where the platform opens one, on a
// channel the caller is let in on.
func TestADirectConversationIsOfferedWhereThePlatformOpensIt(t *testing.T) {
	c := newChats(t)
	c.letIn(t, c.slack, "U0MAKER12", c.maker)
	c.letIn(t, c.telegram, "424242", c.maker)
	c.letIn(t, c.slack, "U0OTHER12", c.other)

	page, err := c.service.Targets(t.Context(), c.who(), TargetQuery{Kind: TargetDirect, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ChannelID != c.slack || page.Items[0].Target.Kind != TargetDirect {
		t.Errorf("direct = %+v, want only Slack's", page.Items)
	}
}

func TestPlacesComeAPageAtATime(t *testing.T) {
	c := newChats(t)
	now := time.Now()
	c.clock = now
	for i := range 3 {
		c.session(t, c.slack, "D0PAGE"+string(rune('A'+i)), "direct", &c.maker, now.Add(-time.Duration(i)*time.Hour))
	}
	first, err := c.service.Targets(t.Context(), c.who(), TargetQuery{Kind: TargetChat, Limit: 2})
	if err != nil || len(first.Items) != 2 || !first.More {
		t.Fatalf("first page = %d items, more %v, %v", len(first.Items), first.More, err)
	}
	rest, _ := c.service.Targets(t.Context(), c.who(), TargetQuery{Kind: TargetChat, Offset: 2, Limit: 2})
	if len(rest.Items) != 1 || rest.More || rest.Items[0].Target == first.Items[0].Target {
		t.Errorf("second page = %+v", rest)
	}
	if _, err := c.service.Targets(t.Context(), c.who(), TargetQuery{Kind: "elsewhere", Limit: 2}); KindOf(err) != Invalid {
		t.Errorf("an unknown kind = %v, want refused", err)
	}
}

// A schedule answering in a chat shares the chat's channel and ref, and a
// person writing there is still in their own conversation.
func TestAScheduleInAChatSharesItAndAMessageStillFindsThePersons(t *testing.T) {
	c := newChats(t)
	now := time.Now()
	c.clock = now
	mine := c.session(t, c.slack, "D0MAKERDM", "direct", &c.maker, now.Add(-time.Hour))
	in := hourly("in my DM")
	in.Target = Target{Kind: TargetChat, SessionID: mine}
	v := c.make(t, in)

	var channel uuid.UUID
	var ref, kind string
	c.scan(t, &channel, `SELECT channel_id FROM sessions WHERE id = $1`, v.SessionID)
	c.scan(t, &ref, `SELECT provider_ref FROM sessions WHERE id = $1`, v.SessionID)
	c.scan(t, &kind, `SELECT kind FROM sessions WHERE id = $1`, v.SessionID)
	if v.SessionID == mine || channel != c.slack || ref != "D0MAKERDM" || kind != "direct" {
		t.Errorf("its conversation is %s on %s %q %s, want its own row in the maker's DM", v.SessionID, channel, ref, kind)
	}
	if v.Target != TargetChat || v.TargetLabel != "Acme Slack · Your direct messages" {
		t.Errorf("target %q label %q", v.Target, v.TargetLabel)
	}
	found, err := c.store.EnsureSession(t.Context(), db.EnsureSessionParams{
		TeamID: c.team, ChannelID: c.slack, ProviderRef: "D0MAKERDM", Kind: "direct", UserID: &c.maker,
	})
	if err != nil || found.ID != mine {
		t.Errorf("a message in the DM found %v, %v; want the person's own conversation", found.ID, err)
	}

	// Its runs answer in the chat.
	c.clock = *v.NextFireAt
	if _, err := c.firer.FireDue(t.Context(), 100, time.Minute); err != nil {
		t.Fatal(err)
	}
	run := c.onlyRun(t, v.ID)
	var reply bool
	c.scan(t, &reply, `SELECT reply_to_channel FROM runs WHERE id = $1`, run)
	c.finish(t, run, "succeeded", "The release went out at nine.")
	if !reply || c.count(t, `SELECT count(*) FROM replies WHERE run_id = $1 AND kind = 'answer'`, run) != 1 {
		t.Errorf("reply_to_channel %v and %d answers queued, want the answer sent to the chat",
			reply, c.count(t, `SELECT count(*) FROM replies WHERE run_id = $1`, run))
	}
	// And the person's conversation reads what it said.
	posted, err := c.store.ListPostedTriggerAnswers(t.Context(), db.ListPostedTriggerAnswersParams{
		SessionID: mine, After: time.Time{}, Before: time.Now().Add(time.Minute), PageSize: 10,
	})
	if err != nil || len(posted) != 1 || posted[0].Name != "in my DM" || posted[0].Text != "The release went out at nine." {
		t.Errorf("posted = %+v, %v", posted, err)
	}

	// Its maker's DM is theirs alone: it is not handed on.
	c.exec(t, `UPDATE schedules SET status = 'paused', paused_reason = 'maker_gone', next_fire_at = NULL WHERE id = $1`, v.ID)
	_, err = c.service.TakeOver(t.Context(), Who{Team: c.team, User: c.other, Manage: true}, Ref{ID: v.ID})
	kindIs(t, err, Conflict, "direct messages")
}

// Posting at a channel's top level takes channel:write and is checked on the
// platform again when made; each post roots a thread whose history reads it.
func TestAChannelPostIsCheckedAgainAndItsThreadReadsIt(t *testing.T) {
	c := newChats(t)
	in := hourly("release notes")
	in.Target = Target{Kind: TargetChannel, ChannelID: c.slack, Ref: "C0RELEASE"}
	_, err := c.service.CreateSchedule(t.Context(), c.who(), Origin{}, in)
	kindIs(t, err, Forbidden, "channel:write")

	writer := c.who()
	writer.ChannelWrite = true
	c.plat.nameErr = &gateway.Refusal{Msg: "the app is not in that channel: invite it first, with /invite"}
	_, err = c.service.CreateSchedule(t.Context(), writer, Origin{}, in)
	kindIs(t, err, Invalid, "/invite")

	c.plat.nameErr = nil
	v, err := c.service.CreateSchedule(t.Context(), writer, Origin{}, in)
	if err != nil {
		t.Fatal(err)
	}
	var ref, kind string
	var user *uuid.UUID
	c.scan(t, &ref, `SELECT provider_ref FROM sessions WHERE id = $1`, v.SessionID)
	c.scan(t, &kind, `SELECT kind FROM sessions WHERE id = $1`, v.SessionID)
	c.scan(t, &user, `SELECT user_id FROM sessions WHERE id = $1`, v.SessionID)
	if ref != "C0RELEASE" || kind != "group" || user != nil || v.TargetLabel != "Acme Slack · #chan-c0release" {
		t.Errorf("session %q %s %v, label %q", ref, kind, user, v.TargetLabel)
	}

	// A fire's answer lands at the top level; a reply under it is a thread.
	c.clock = *v.NextFireAt
	if _, err := c.firer.FireDue(t.Context(), 100, time.Minute); err != nil {
		t.Fatal(err)
	}
	run := c.onlyRun(t, v.ID)
	c.finish(t, run, "succeeded", "Three changes went out.")
	c.exec(t, `UPDATE replies SET posted_ref = 'C0RELEASE:1700000000.000100' WHERE run_id = $1`, run)
	thread := c.session(t, c.slack, "C0RELEASE:1700000000.000100", "thread", &c.other, time.Now())
	posted, err := c.store.ListPostedTriggerAnswers(t.Context(), db.ListPostedTriggerAnswersParams{
		SessionID: thread, After: time.Time{}, Before: time.Now().Add(time.Minute), PageSize: 10,
	})
	if err != nil || len(posted) != 1 || posted[0].Text != "Three changes went out." {
		t.Errorf("the thread reads %+v, %v; want the post it hangs from", posted, err)
	}
}

// A direct conversation is opened on the platform for the caller's own id
// there; the platform's no reaches them as it said it.
func TestADirectScheduleOpensTheMakersConversation(t *testing.T) {
	c := newChats(t)
	c.letIn(t, c.slack, "U0MAKER12", c.maker)
	in := hourly("to me")
	in.Target = Target{Kind: TargetDirect, ChannelID: c.slack}

	c.plat.openErr = &gateway.Refusal{Msg: "the Slack app needs the im:write scope to message you directly"}
	_, err := c.service.CreateSchedule(t.Context(), c.who(), Origin{}, in)
	kindIs(t, err, Invalid, "im:write")
	if n := c.count(t, `SELECT count(*) FROM schedules`); n != 0 {
		t.Errorf("%d schedules after a refusal, want none", n)
	}

	c.plat.openErr = nil
	v := c.make(t, in)
	var ref string
	var user uuid.UUID
	c.scan(t, &ref, `SELECT provider_ref FROM sessions WHERE id = $1`, v.SessionID)
	c.scan(t, &user, `SELECT user_id FROM sessions WHERE id = $1`, v.SessionID)
	if ref != "D00MAKER12" || user != c.maker || c.plat.opened[len(c.plat.opened)-1] != "U0MAKER12" {
		t.Errorf("session %q with %s after opening %v", ref, user, c.plat.opened)
	}
	in.Target.ChannelID = c.telegram
	_, err = c.service.CreateSchedule(t.Context(), c.who(), Origin{}, in)
	kindIs(t, err, Invalid, "cannot open a direct conversation")
}
