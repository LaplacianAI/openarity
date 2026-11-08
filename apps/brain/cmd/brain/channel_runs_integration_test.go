package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/config"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/custom"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets/static"
)

// hook posts one message to a custom channel's webhook, signed as the provider
// would sign it, and returns the status the brain answered.
func hook(t *testing.T, webhook string, channel uuid.UUID, secret string, msg map[string]any) int {
	t.Helper()

	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		"http://"+webhook+"/hooks/custom/"+channel.String(), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(custom.HeaderTimestamp, ts)
	req.Header.Set(custom.HeaderSignature, custom.Sign(secret, ts, body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST hook: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func asks(id, text string) map[string]any {
	return map[string]any{
		"id":      id,
		"text":    text,
		"author":  map[string]any{"ref": "U-asha", "display_name": "Asha"},
		"session": map[string]any{"ref": "D-asha", "kind": "direct"},
	}
}

// waitForReplies polls the outbox until it holds n replies, or fails.
func (b *brain) waitForReplies(t *testing.T, n int, within time.Duration) {
	t.Helper()

	deadline := time.Now().Add(within)
	for b.count(t, `SELECT count(*) FROM replies`) < n {
		if time.Now().After(deadline) {
			t.Fatalf("the outbox holds %d replies after %s, want %d", b.count(t, `SELECT count(*) FROM replies`), within, n)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// The whole of it, through the binary as it ships: a signed message on a
// channel starts the team's hub, the worker runs it against the model, and the
// answer is in the outbox for the conversation. The same message delivered
// again starts nothing; the next message is answered knowing what was said.
func TestAChannelMessageIsAnsweredInItsConversation(t *testing.T) {
	b := newBrain(t, newScriptedLLM(t))
	webhook := os.Getenv("OPENARITY_WEBHOOK_BIND")

	var dev uuid.UUID
	if err := b.raw.QueryRow(t.Context(),
		`SELECT id FROM users WHERE issuer = 'dev' AND subject = 'dev'`).Scan(&dev); err != nil {
		t.Fatalf("the development user: %v", err)
	}
	if _, err := b.raw.Exec(t.Context(),
		`UPDATE team_members SET role = 'admin' WHERE team_id = $1 AND user_id = $2`, b.team, dev); err != nil {
		t.Fatalf("making the development user an admin: %v", err)
	}
	if _, err := b.raw.Exec(t.Context(), `
		INSERT INTO team_settings (team_id, model_name, model_max_tokens) VALUES ($1, 'test/model', 1024)`,
		b.team); err != nil {
		t.Fatalf("giving the hub a model: %v", err)
	}

	status, raw := b.call(t, http.MethodPost, "/teams/"+b.team.String()+"/channels",
		map[string]string{"provider": "custom", "name": "support"})
	if status != http.StatusCreated {
		t.Fatalf("POST channels: %d %s", status, raw)
	}
	var ch struct {
		ID            uuid.UUID `json:"id"`
		SigningSecret string    `json:"signing_secret"`
	}
	if err := json.Unmarshal(raw, &ch); err != nil || ch.SigningSecret == "" {
		t.Fatalf("decode the channel: %v (%s)", err, raw)
	}
	if _, err := b.raw.Exec(t.Context(),
		`INSERT INTO channel_senders (channel_id, sender_ref, user_id) VALUES ($1, 'U-asha', $2)`, ch.ID, dev); err != nil {
		t.Fatalf("approving the sender: %v", err)
	}

	stopWorker := startWorker(t)
	defer stopWorker()

	if got := hook(t, webhook, ch.ID, ch.SigningSecret, asks("m-1", "Why did the export fail?")); got != http.StatusOK {
		t.Fatalf("the first message was answered %d, want 200", got)
	}
	b.waitForReplies(t, 1, 90*time.Second)

	var kind, text string
	if err := b.raw.QueryRow(t.Context(), `SELECT kind, text FROM replies`).Scan(&kind, &text); err != nil {
		t.Fatalf("reading the reply: %v", err)
	}
	if kind != "answer" || text != finalAnswer {
		t.Errorf("reply = %s %q, want the model's answer %q", kind, text, finalAnswer)
	}

	// The provider did not see the 200, and sends it again.
	if got := hook(t, webhook, ch.ID, ch.SigningSecret, asks("m-1", "Why did the export fail?")); got != http.StatusOK {
		t.Fatalf("the redelivery was answered %d, want 200", got)
	}
	if n := b.count(t, `SELECT count(*) FROM runs WHERE reply_to_channel`); n != 1 {
		t.Errorf("%d channel runs after a redelivery, want 1", n)
	}

	if got := hook(t, webhook, ch.ID, ch.SigningSecret, asks("m-2", "Was it the nightly one?")); got != http.StatusOK {
		t.Fatalf("the second message was answered %d, want 200", got)
	}
	b.waitForReplies(t, 2, 90*time.Second)
	if !b.llm.saw("The conversation so far") || !b.llm.saw("Why did the export fail?") {
		t.Error("the second run was not shown the conversation before it")
	}
	if n := b.count(t, `SELECT count(*) FROM runs WHERE reply_to_channel AND status = 'succeeded'`); n != 2 {
		t.Errorf("%d channel runs succeeded, want 2", n)
	}
}

// replyReceiver is the provider's end of a custom channel: it checks each
// reply's signature with the channel's secret and keeps the ones that pass.
type replyReceiver struct {
	*httptest.Server
	secret string

	mu      sync.Mutex
	replies []map[string]any
	forged  int
}

func newReplyReceiver(t *testing.T) *replyReceiver {
	t.Helper()
	r := &replyReceiver{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		ts := req.Header.Get(custom.HeaderTimestamp)
		r.mu.Lock()
		defer r.mu.Unlock()
		if req.Method != http.MethodPost || req.Header.Get(custom.HeaderSignature) != custom.SignReply(r.secret, ts, body) {
			r.forged++
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var reply map[string]any
		_ = json.Unmarshal(body, &reply)
		r.replies = append(r.replies, reply)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *replyReceiver) received() ([]map[string]any, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.replies), r.forged
}

// The other half of the loop, through the binary: the answer to a message is
// posted to the channel's reply URL, signed, as soon as the run finishes. A
// channel whose URL was cleared sends nothing, and says why on the reply.
func TestAnAnswerIsPostedBackToTheChannel(t *testing.T) {
	// One process plays serve and the worker, so they share the secret store
	// two processes would reach through OpenBao.
	shared := static.New()
	openSecretStore = func(*config.Config, *slog.Logger) secrets.Store { return shared }
	t.Cleanup(func() { openSecretStore = newSecretStore })

	b := newBrain(t, newScriptedLLM(t))
	webhook := os.Getenv("OPENARITY_WEBHOOK_BIND")
	rcv := newReplyReceiver(t)

	var dev uuid.UUID
	if err := b.raw.QueryRow(t.Context(),
		`SELECT id FROM users WHERE issuer = 'dev' AND subject = 'dev'`).Scan(&dev); err != nil {
		t.Fatalf("the development user: %v", err)
	}
	if _, err := b.raw.Exec(t.Context(),
		`UPDATE team_members SET role = 'admin' WHERE team_id = $1 AND user_id = $2`, b.team, dev); err != nil {
		t.Fatalf("making the development user an admin: %v", err)
	}
	if _, err := b.raw.Exec(t.Context(), `
		INSERT INTO team_settings (team_id, model_name, model_max_tokens) VALUES ($1, 'test/model', 1024)`,
		b.team); err != nil {
		t.Fatalf("giving the hub a model: %v", err)
	}

	status, raw := b.call(t, http.MethodPost, "/teams/"+b.team.String()+"/channels",
		map[string]string{"provider": "custom", "name": "support", "reply_url": rcv.URL + "/replies"})
	if status != http.StatusCreated {
		t.Fatalf("POST channels: %d %s", status, raw)
	}
	var ch struct {
		ID            uuid.UUID `json:"id"`
		SigningSecret string    `json:"signing_secret"`
		ReplyURL      string    `json:"reply_url"`
	}
	if err := json.Unmarshal(raw, &ch); err != nil || ch.ReplyURL != rcv.URL+"/replies" {
		t.Fatalf("the channel: %v (%s)", err, raw)
	}
	rcv.mu.Lock()
	rcv.secret = ch.SigningSecret
	rcv.mu.Unlock()
	if _, err := b.raw.Exec(t.Context(),
		`INSERT INTO channel_senders (channel_id, sender_ref, user_id) VALUES ($1, 'U-asha', $2)`, ch.ID, dev); err != nil {
		t.Fatalf("approving the sender: %v", err)
	}

	startWorker(t)

	if got := hook(t, webhook, ch.ID, ch.SigningSecret, asks("m-1", "Why did the export fail?")); got != http.StatusOK {
		t.Fatalf("the message was answered %d, want 200", got)
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		if got, _ := rcv.received(); len(got) > 0 {
			break
		}
		if time.Now().After(deadline) {
			var st, le string
			var att int
			_ = b.raw.QueryRow(t.Context(), `SELECT status, attempts, coalesce(last_error,'') FROM replies`).Scan(&st, &att, &le)
			_, f := rcv.received()
			t.Fatalf("no reply reached the channel within 90s: reply %s attempts %d err %q forged %d", st, att, le, f)
		}
		time.Sleep(200 * time.Millisecond)
	}

	got, forged := rcv.received()
	var run uuid.UUID
	if err := b.raw.QueryRow(t.Context(), `SELECT id FROM runs WHERE reply_to_channel`).Scan(&run); err != nil {
		t.Fatalf("the run: %v", err)
	}
	session, _ := got[0]["session"].(map[string]any)
	if len(got) != 1 || forged != 0 || got[0]["id"] != run.String() || got[0]["text"] != finalAnswer ||
		got[0]["kind"] != "answer" || got[0]["in_reply_to"] != "m-1" || session["ref"] != "D-asha" {
		t.Errorf("the channel received %v (%d forged), want the run's answer to m-1", got, forged)
	}
	if n := b.count(t, `SELECT count(*) FROM replies WHERE status = 'sent' AND attempts = 1`); n != 1 {
		t.Errorf("%d replies sent on their first attempt, want 1", n)
	}
	// Sent by the finish's kick, not left for the sweep.
	if n := b.count(t, `SELECT count(*) FROM dbos.workflow_status WHERE workflow_uuid = 'reply:' || $1::text`, run); n != 1 {
		t.Errorf("%d kick workflows for run %s, want 1", n, run)
	}

	status, raw = b.call(t, http.MethodPatch, "/teams/"+b.team.String()+"/channels/"+ch.ID.String(),
		map[string]any{"reply_url": nil})
	if status != http.StatusOK {
		t.Fatalf("PATCH channel: %d %s", status, raw)
	}
	if got := hook(t, webhook, ch.ID, ch.SigningSecret, asks("m-2", "Was it the nightly one?")); got != http.StatusOK {
		t.Fatalf("the second message was answered %d, want 200", got)
	}
	deadline = time.Now().Add(90 * time.Second)
	for b.count(t, `SELECT count(*) FROM replies WHERE status = 'failed'`) < 1 {
		if time.Now().After(deadline) {
			t.Fatal("the reply to a channel with no URL never failed")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if n := b.count(t, `SELECT count(*) FROM replies WHERE status = 'failed' AND attempts = 1 AND last_error LIKE '%no reply URL%'`); n != 1 {
		t.Errorf("%d replies failed at once for want of a URL, want 1", n)
	}
	if got, _ := rcv.received(); len(got) != 1 {
		t.Errorf("the channel received %d replies after its URL was cleared, want still 1", len(got))
	}
}
