package channels

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/authz"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/custom"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// receiveOnly is the custom adapter with Reply taken away: a provider that has
// nowhere to post back.
type receiveOnly struct{ gateway.Provider }

func channelPath(team, ch uuid.UUID) string {
	return "/teams/" + team.String() + "/channels/" + ch.String()
}

func TestACreatedChannelKeepsWhereItsRepliesGo(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := &fakeStore{}
	rec := call(t, s, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
		http.MethodPost, "/teams/"+teamID.String()+"/channels",
		`{"provider":"custom","name":"support","reply_url":"  https://support.example.com/replies  "}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	if len(s.created) != 1 || s.created[0].ReplyUrl == nil || *s.created[0].ReplyUrl != "https://support.example.com/replies" {
		t.Errorf("stored %+v, want the URL trimmed", s.created)
	}
	var out channel
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.ReplyURL != "https://support.example.com/replies" {
		t.Errorf("response reply_url = %q (%v), want the URL", out.ReplyURL, err)
	}
}

// A channel created without one shows none: absent, not "".
func TestAChannelWithNoReplyURLShowsNone(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	rec := call(t, &fakeStore{}, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
		http.MethodPost, "/teams/"+teamID.String()+"/channels", `{"provider":"custom","name":"support"}`)
	if strings.Contains(rec.Body.String(), `"reply_url"`) {
		t.Errorf("response %s names a reply_url the channel does not have", rec.Body)
	}
}

// Every URL the brain must not post to, refused before a row is written.
func TestARefusedReplyURLCreatesNothing(t *testing.T) {
	t.Parallel()

	for name, raw := range map[string]string{
		"no scheme":       "support.example.com/replies",
		"ftp":             "ftp://support.example.com/replies",
		"no host":         "https:///replies",
		"credentials":     "https://user:pass@support.example.com/replies",
		"a query":         "https://support.example.com/replies?token=abc",
		"an empty query":  "https://support.example.com/replies?",
		"a fragment":      "https://support.example.com/replies#x",
		"loopback":        "http://127.0.0.1:8080/replies",
		"loopback v6":     "http://[::1]/replies",
		"mapped loopback": "http://[::ffff:127.0.0.1]/replies",
		"private":         "http://10.0.0.5/replies",
		"metadata":        "http://169.254.169.254/latest",
		"too long":        "https://support.example.com/" + strings.Repeat("a", maxReplyURLBytes),
		"blank":           "   ",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			teamID := uuid.New()
			s := &fakeStore{}
			body, _ := json.Marshal(map[string]string{"provider": "custom", "name": "support", "reply_url": raw})
			rec := call(t, s, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
				http.MethodPost, "/teams/"+teamID.String()+"/channels", string(body))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("POST with reply_url %q = %d, want 400 (%s)", raw, rec.Code, rec.Body)
			}
			if len(s.created) != 0 {
				t.Errorf("a refused reply_url still created %d channels", len(s.created))
			}
		})
	}
}

// A provider with no Replier has nothing to do with a URL; accepting one would
// let a person think their channel replies.
func TestAProviderThatCannotReplyTakesNoReplyURL(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	s := &fakeStore{}
	p := providers{"custom": receiveOnly{custom.New()}}
	rec := callWith(t, p, "", s, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
		http.MethodPost, "/teams/"+teamID.String()+"/channels",
		`{"provider":"custom","name":"support","reply_url":"https://support.example.com/replies"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "sends no replies") {
		t.Errorf("POST = %d %q, want 400 naming the provider", rec.Code, rec.Body)
	}
	if len(s.created) != 0 {
		t.Errorf("created %d channels", len(s.created))
	}
}

func TestUpdatingAChannelSetsAndClearsItsReplyURL(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	ch := db.Channel{ID: uuid.New(), TeamID: teamID, Provider: "custom", Name: "support"}
	s := &fakeStore{channels: []db.Channel{ch}}
	a := &fakeAuthz{allowed: true}

	rec := call(t, s, newFakeSecrets(), a, memberOf(teamID, "admin"),
		http.MethodPatch, channelPath(teamID, ch.ID), `{"reply_url":"https://support.example.com/replies"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"reply_url":"https://support.example.com/replies"`) {
		t.Fatalf("PATCH = %d %s, want 200 with the URL", rec.Code, rec.Body)
	}
	if got := s.channels[0].ReplyUrl; got == nil || *got != "https://support.example.com/replies" {
		t.Errorf("stored %v, want the URL", got)
	}
	if len(a.asked) != 1 || a.asked[0] != authz.Action("channel:write") {
		t.Errorf("the guard asked for %v, want channel:write", a.asked)
	}

	for _, clear := range []string{`{"reply_url":null}`, `{"reply_url":""}`, `{"reply_url":"  "}`} {
		url := "https://support.example.com/replies"
		s.channels[0].ReplyUrl = &url
		rec = call(t, s, newFakeSecrets(), a, memberOf(teamID, "admin"),
			http.MethodPatch, channelPath(teamID, ch.ID), clear)
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"reply_url"`) {
			t.Fatalf("PATCH %s = %d %s, want 200 with no URL", clear, rec.Code, rec.Body)
		}
		if s.channels[0].ReplyUrl != nil {
			t.Errorf("stored %q after %s, want none", *s.channels[0].ReplyUrl, clear)
		}
	}
}

// An empty body would otherwise read as "clear it". Clearing is said with null.
func TestAnUpdateMustSayWhatTheReplyURLIs(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	url := "https://support.example.com/replies"
	ch := db.Channel{ID: uuid.New(), TeamID: teamID, Provider: "custom", Name: "support", ReplyUrl: &url}
	s := &fakeStore{channels: []db.Channel{ch}}

	for _, body := range []string{`{}`, `{"reply_url":"http://10.0.0.1/x"}`, `{"reply_url":7}`} {
		rec := call(t, s, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
			http.MethodPatch, channelPath(teamID, ch.ID), body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("PATCH %s = %d, want 400", body, rec.Code)
		}
		if got := s.channels[0].ReplyUrl; got == nil || *got != url {
			t.Errorf("PATCH %s changed the URL to %v", body, got)
		}
	}
}

// The guard checked the team in the path, not the channel. Another team's
// channel answers as one that does not exist, and is left alone.
func TestAnUpdateCannotReachAnotherTeamsChannel(t *testing.T) {
	t.Parallel()

	mine, theirs := uuid.New(), uuid.New()
	ch := db.Channel{ID: uuid.New(), TeamID: theirs, Provider: "custom", Name: "support"}
	s := &fakeStore{channels: []db.Channel{ch}}

	rec := call(t, s, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(mine, "admin"),
		http.MethodPatch, channelPath(mine, ch.ID), `{"reply_url":"https://attacker.example.com/x"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("PATCH = %d, want 404", rec.Code)
	}
	if s.channels[0].ReplyUrl != nil {
		t.Errorf("another team's channel now replies to %q", *s.channels[0].ReplyUrl)
	}
}

func TestAnUpdateWithoutChannelWriteIsForbidden(t *testing.T) {
	t.Parallel()

	teamID := uuid.New()
	ch := db.Channel{ID: uuid.New(), TeamID: teamID, Provider: "custom", Name: "support"}
	s := &fakeStore{channels: []db.Channel{ch}}

	rec := call(t, s, newFakeSecrets(), &fakeAuthz{allowed: false}, memberOf(teamID, "member"),
		http.MethodPatch, channelPath(teamID, ch.ID), `{"reply_url":"https://support.example.com/replies"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("PATCH = %d, want 403", rec.Code)
	}
	if s.channels[0].ReplyUrl != nil {
		t.Error("a refused caller set the reply URL")
	}
}

// A client offers a reply URL only where the provider can use one.
func TestAChannelSaysWhetherItsProviderCanReply(t *testing.T) {
	t.Parallel()

	for name, p := range map[string]Providers{
		"replies":       providers{"custom": custom.New()},
		"never replies": providers{"custom": receiveOnly{custom.New()}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			teamID := uuid.New()
			rec := callWith(t, p, "", &fakeStore{}, newFakeSecrets(), &fakeAuthz{allowed: true}, memberOf(teamID, "admin"),
				http.MethodPost, "/teams/"+teamID.String()+"/channels", `{"provider":"custom","name":"support"}`)
			var out channel
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatalf("decode %s: %v", rec.Body, err)
			}
			if want := name == "replies"; out.CanReply != want || !strings.Contains(rec.Body.String(), `"can_reply"`) {
				t.Errorf("can_reply = %v in %s, want %v, always present", out.CanReply, rec.Body, want)
			}
		})
	}
}
