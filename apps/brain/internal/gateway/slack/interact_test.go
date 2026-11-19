package slack_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/slack"
)

const (
	hookURL = "https://hooks.slack.com/actions/T061EG9R6/1234567890/AbCdEfGhIjKl"
	thread  = "C2147483705:1763544601.000300"
)

// formOf is an interactivity body as Slack posts it: one field, payload.
func formOf(payload string) string { return url.Values{"payload": {payload}}.Encode() }

// interactive is a delivery to the Interactivity URL, signed.
func interactive(body string, ts time.Time) gateway.WebhookRequest {
	req := signed(body, ts)
	req.Suffix = slack.InteractivitySuffix
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func tapPayload(action, value string) string {
	return `{"type":"block_actions","user":{"id":"U2147483697","username":"asha","name":"asha"},` +
		`"api_app_id":"A0PNCHHK2","trigger_id":"13345224609.738474920.8088930838d88f008e0",` +
		`"container":{"type":"message","message_ts":"1763544800.000700","channel_id":"C2147483705","is_ephemeral":false,"thread_ts":"1763544601.000300"},` +
		`"channel":{"id":"C2147483705","name":"ops"},` +
		`"message":{"type":"message","ts":"1763544800.000700","thread_ts":"1763544601.000300"},` +
		`"response_url":"` + hookURL + `",` +
		`"actions":[{"action_id":"` + action + `","block_id":"approval:ap-1","value":"` + value + `","type":"button"}]}`
}

func parseTap(t *testing.T, payload string) (gateway.Result, error) {
	t.Helper()
	return slack.New().Parse(interactive(formOf(payload), receivedAt), creds())
}

func tap(t *testing.T, payload string) gateway.Inbound {
	t.Helper()
	got, err := parseTap(t, payload)
	if err != nil || len(got.Messages) != 1 {
		t.Fatalf("Parse: %+v, %v; want one message", got, err)
	}
	if err := got.Messages[0].Validate(); err != nil {
		t.Fatalf("the tap is not a valid message: %v", err)
	}
	return got.Messages[0]
}

func TestSlackDeclaresTheInteractivityRoute(t *testing.T) {
	var found bool
	for _, r := range slack.New().Routes() {
		if r.Method == http.MethodPost && r.Suffix == "/interactivity" {
			found = true
		}
	}
	if !found {
		t.Errorf("routes %+v have no POST /interactivity", slack.New().Routes())
	}
}

// Each button is a decision on the request its value names, in the thread it
// was tapped in, by the person who tapped.
func TestATapIsAnAnswerInItsThread(t *testing.T) {
	for _, tc := range []struct {
		action string
		want   gateway.Decision
		text   string
	}{
		{"openarity_approve", gateway.Approve, "Approve"},
		{"openarity_approve_run", gateway.ApproveRun, "Approve for this run"},
		{"openarity_decline", gateway.DeclineAsk, "Decline"},
	} {
		in := tap(t, tapPayload(tc.action, "ap-1"))
		a := in.Answer
		if a == nil || a.ApprovalID != "ap-1" || a.Decision != tc.want || a.Reason != "" {
			t.Errorf("%s: answer %+v", tc.action, a)
		}
		if in.Text != tc.text || in.Author.Ref != "U2147483697" || in.Author.IsBot {
			t.Errorf("%s: %+v", tc.action, in)
		}
		if in.Session.Ref != thread || in.Session.Kind != gateway.SessionThread {
			t.Errorf("%s: session %+v", tc.action, in.Session)
		}
		if in.ExternalID != "tap:13345224609.738474920.8088930838d88f008e0" {
			t.Errorf("%s: external id %q", tc.action, in.ExternalID)
		}
		var h map[string]string
		if err := json.Unmarshal(a.Handle, &h); err != nil || h["response_url"] != hookURL ||
			h["trigger_id"] != "13345224609.738474920.8088930838d88f008e0" || h["ref"] != thread || h["kind"] != "thread" {
			t.Errorf("%s: handle %s", tc.action, a.Handle)
		}
	}
}

// Two taps are two messages: the id is the interaction's, not the request's.
func TestTwoTapsHaveTwoIDs(t *testing.T) {
	a := tap(t, tapPayload("openarity_approve", "ap-1"))
	b := tap(t, strings.Replace(tapPayload("openarity_approve", "ap-1"), "13345224609", "13345224610", 1))
	if a.ExternalID == b.ExternalID {
		t.Errorf("both taps are %q", a.ExternalID)
	}
}

// A direct message is one conversation, threads or not: a tap in it is the
// DM's, as its messages are.
func TestATapInADirectMessageIsTheDirectSession(t *testing.T) {
	p := strings.NewReplacer(`"C2147483705"`, `"D024BE91L"`).Replace(tapPayload("openarity_approve", "ap-1"))
	in := tap(t, p)
	if in.Session.Ref != "D024BE91L" || in.Session.Kind != gateway.SessionDirect {
		t.Errorf("session %+v", in.Session)
	}
}

// What the signature guards: a delivery that is not Slack's, on the new route
// as on the old one.
func TestTheInteractivityRouteRefusesForgeries(t *testing.T) {
	p := slack.New()
	body := formOf(tapPayload("openarity_approve", "ap-1"))
	if err := p.Verify(interactive(body, receivedAt), creds()); err != nil {
		t.Fatalf("a genuine delivery refused: %v", err)
	}
	forged := interactive(body, receivedAt)
	forged.Header.Set(slack.SignatureHeader, "v0=00"+strings.Repeat("a", 62))
	tampered := interactive(body, receivedAt)
	tampered.Body = []byte(formOf(tapPayload("openarity_approve", "ap-2")))
	unsigned := interactive(body, receivedAt)
	unsigned.Header.Del(slack.SignatureHeader)
	stale := interactive(body, receivedAt.Add(-time.Hour))
	otherSecret := creds()
	otherSecret[gateway.KeySigning] = "another-secret"
	for name, tc := range map[string]struct {
		req   gateway.WebhookRequest
		creds gateway.Credentials
	}{
		"forged signature": {forged, creds()}, "tampered body": {tampered, creds()},
		"no signature": {unsigned, creds()}, "stale": {stale, creds()}, "wrong secret": {interactive(body, receivedAt), otherSecret},
	} {
		if err := p.Verify(tc.req, tc.creds); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Bodies a tap could never be, or that are Slack's but nothing of ours.
func TestHostileInteractionsDoNothing(t *testing.T) {
	good := tapPayload("openarity_approve", "ap-1")
	swap := func(old, new string) string { return strings.Replace(good, old, new, 1) }
	for name, tc := range map[string]struct {
		payload string
		err     bool
	}{
		"not json":                  {"{", true},
		"empty":                     {"", true},
		"a list":                    {"[1]", true},
		"unknown type":              {`{"type":"shortcut"}`, false},
		"no type":                   {`{}`, false},
		"another button":            {swap("openarity_approve", "someone_elses"), false},
		"no actions":                {`{"type":"block_actions","user":{"id":"U2147483697"}}`, false},
		"an ephemeral message":      {swap(`"is_ephemeral":false`, `"is_ephemeral":true`), false},
		"empty value":               {tapPayload("openarity_approve", ""), true},
		"huge value":                {tapPayload("openarity_approve", strings.Repeat("a", 201)), true},
		"no trigger id":             {swap(`"trigger_id":"13345224609.738474920.8088930838d88f008e0"`, `"trigger_id":""`), true},
		"no user":                   {swap(`"id":"U2147483697"`, `"id":""`), true},
		"a user that is a mass":     {swap(`"id":"U2147483697"`, `"id":"here"`), true},
		"http response url":         {swap("https://hooks", "http://hooks"), true},
		"another host":              {swap("hooks.slack.com/", "hooks.slack.com.evil.example/"), true},
		"userinfo in the url":       {swap("https://hooks", "https://hooks.slack.com@evil.example#"), true},
		"userinfo before slack":     {swap("https://hooks", "https://u:p@hooks"), true},
		"no response url":           {swap(`"response_url":"`+hookURL+`"`, `"response_url":""`), true},
		"a channel that is a path":  {strings.NewReplacer(`"C2147483705"`, `"C1/../x"`).Replace(good), false},
		"a thread that is not a ts": {strings.ReplaceAll(good, `"thread_ts":"1763544601.000300"`, `"thread_ts":"x:y"`), false},
		"a channel top-level":       {strings.NewReplacer(`,"thread_ts":"1763544601.000300"`, ``).Replace(good), false},
	} {
		got, err := parseTap(t, tc.payload)
		if (err != nil) != tc.err {
			t.Errorf("%s: error %v, want error %v", name, err, tc.err)
		}
		if len(got.Messages) != 0 || len(got.Ack) != 0 {
			t.Errorf("%s: produced %+v", name, got)
		}
	}
}

func TestHostileInteractionBodies(t *testing.T) {
	p := slack.New()
	for name, body := range map[string]string{
		"no payload field":   "other=1",
		"a json body":        tapPayload("openarity_approve", "ap-1"),
		"a bad escape":       "payload=%zz",
		"too large":          formOf(`{"type":"shortcut","pad":"` + strings.Repeat("a", 300<<10) + `"}`),
		"an events delivery": envelope(`{"type":"message"}`),
	} {
		got, err := p.Parse(interactive(body, receivedAt), creds())
		if err == nil || len(got.Messages) != 0 {
			t.Errorf("%s: %+v, %v", name, got, err)
		}
	}
	// And the other way: a tap is not an event, so the Events route reads
	// nothing of it.
	got, err := p.Parse(request(formOf(tapPayload("openarity_approve", "ap-1"))), creds())
	if err == nil || len(got.Messages) != 0 {
		t.Errorf("a tap on the events route: %+v, %v", got, err)
	}
}

func modalPayload(meta, callback, reason string) string {
	state := `{}`
	if reason != "-" {
		state = `{"reason":{"reason":{"type":"plain_text_input","value":` + jsonString(reason) + `}}}`
	}
	return `{"type":"view_submission","user":{"id":"U2147483697","username":"asha"},"trigger_id":"1.2.3",` +
		`"view":{"id":"V0MODAL1","type":"modal","callback_id":"` + callback + `","private_metadata":` + jsonString(meta) + `,` +
		`"state":{"values":` + state + `}}}`
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

var goodMeta = `{"ref":"` + thread + `","kind":"thread","approval":"ap-1","url":"` + hookURL + `"}`

func TestAModalSubmissionIsADeclineWithItsReason(t *testing.T) {
	in := tap(t, modalPayload(goodMeta, "openarity_decline_reason", "  not on a Friday \n"))
	a := in.Answer
	if a == nil || a.Decision != gateway.Decline || a.ApprovalID != "ap-1" || a.Reason != "not on a Friday" {
		t.Fatalf("answer %+v", a)
	}
	if in.Session.Ref != thread || in.Session.Kind != gateway.SessionThread || in.Author.Ref != "U2147483697" ||
		in.Text != "Decline" || in.ExternalID != "modal:V0MODAL1" {
		t.Errorf("message %+v", in)
	}
	var h map[string]string
	if err := json.Unmarshal(a.Handle, &h); err != nil || h["response_url"] != hookURL || h["trigger_id"] != "" {
		t.Errorf("handle %s", a.Handle)
	}
}

func TestAModalWithNoReasonIsADeclineWithNone(t *testing.T) {
	for _, reason := range []string{"-", "", "   "} {
		a := tap(t, modalPayload(goodMeta, "openarity_decline_reason", reason)).Answer
		if a.Decision != gateway.Decline || a.Reason != "" {
			t.Errorf("reason %q: %+v", reason, a)
		}
	}
}

func TestAReasonIsKeptToWhatTheFormAllows(t *testing.T) {
	a := tap(t, modalPayload(goodMeta, "openarity_decline_reason", strings.Repeat("é", 900))).Answer
	if n := len([]rune(a.Reason)); n != 500 {
		t.Errorf("%d characters kept", n)
	}
}

func TestAModalSubmissionIsAckedWithNothing(t *testing.T) {
	got, err := parseTap(t, modalPayload(goodMeta, "openarity_decline_reason", "no"))
	if err != nil || len(got.Ack) != 0 {
		t.Errorf("ack %q, %v: Slack wants an empty 200", got.Ack, err)
	}
}

func TestHostileModalSubmissions(t *testing.T) {
	meta := func(old, new string) string { return strings.Replace(goodMeta, old, new, 1) }
	for name, tc := range map[string]struct {
		payload string
		err     bool
	}{
		"another modal":               {modalPayload(goodMeta, "someone_elses", "x"), false},
		"no metadata":                 {modalPayload("", "openarity_decline_reason", "x"), true},
		"metadata not json":           {modalPayload("nope", "openarity_decline_reason", "x"), true},
		"no approval":                 {modalPayload(meta(`"approval":"ap-1"`, `"approval":""`), "openarity_decline_reason", "x"), true},
		"a huge approval":             {modalPayload(meta(`"ap-1"`, `"`+strings.Repeat("a", 201)+`"`), "openarity_decline_reason", "x"), true},
		"a ref of no channel":         {modalPayload(meta(`C2147483705:`, `x/y:`), "openarity_decline_reason", "x"), true},
		"a thread kind, no thread":    {modalPayload(meta(`:1763544601.000300`, ``), "openarity_decline_reason", "x"), true},
		"a thread that is no ts":      {modalPayload(meta(`1763544601.000300`, `x`), "openarity_decline_reason", "x"), true},
		"a direct kind, a thread ref": {modalPayload(meta(`"thread"`, `"direct"`), "openarity_decline_reason", "x"), true},
		"a kind that is unknown":      {modalPayload(meta(`"thread"`, `"group"`), "openarity_decline_reason", "x"), true},
		"another host's url":          {modalPayload(meta("hooks.slack.com", "evil.example"), "openarity_decline_reason", "x"), true},
		"no url":                      {modalPayload(meta(hookURL, ""), "openarity_decline_reason", "x"), true},
		"no view":                     {`{"type":"view_submission","user":{"id":"U2147483697"}}`, false},
		"no user":                     {strings.Replace(modalPayload(goodMeta, "openarity_decline_reason", "x"), "U2147483697", "", 1), true},
		"no view id":                  {strings.Replace(modalPayload(goodMeta, "openarity_decline_reason", "x"), `"id":"V0MODAL1",`, "", 1), true},
	} {
		got, err := parseTap(t, tc.payload)
		if (err != nil) != tc.err || len(got.Messages) != 0 {
			t.Errorf("%s: %+v, %v", name, got, err)
		}
	}
}
