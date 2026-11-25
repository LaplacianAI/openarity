package store

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// sentFile is a file a person sent in c, kept as its attachment.
func (c conversation) sentFile(t *testing.T, s *Store, externalID, key string) db.Attachment {
	t.Helper()
	m := c.says(t, s, externalID, "here is the export")
	arg := attachment(t, s, m, c.session.ID, key)
	arg.Filename, arg.MediaType = "export.csv", "text/csv"
	return mustCreateAttachment(t, s, arg)
}

func sendFile(t *testing.T, s *Store, run, file uuid.UUID, call string, detail []byte) int64 {
	t.Helper()
	n, err := s.InsertFileReply(t.Context(), db.InsertFileReplyParams{RunID: run, AttachmentID: file, CallID: call, Detail: detail})
	if err != nil {
		t.Fatalf("InsertFileReply: %v", err)
	}
	return n
}

// A run hands its person a file of its conversation: one reply, named by its
// file and its call, which the sender claims carrying both and reads the
// file of. The same call again is still one reply.
func TestAFileIsOneReplyForItsCall(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "file-once")
	file := c.sentFile(t, s, "m-file", "teams/x/objects/export")
	r := c.working(t, s, "m-1")

	if n := sendFile(t, s, r.ID, file.ID, "call-1", []byte(`{"caption":"Q3"}`)); n != 1 {
		t.Fatalf("wrote %d replies, want 1", n)
	}
	if n := sendFile(t, s, r.ID, file.ID, "call-1", nil); n != 0 {
		t.Errorf("the same call wrote %d more", n)
	}
	if n := sendFile(t, s, r.ID, file.ID, "call-2", nil); n != 1 {
		t.Errorf("another call sending the same file wrote %d, want 1", n)
	}

	rows := claimReplies(t, s.Queries, 60)
	if len(rows) != 1 {
		t.Fatalf("claimed %d, want the conversation's first", len(rows))
	}
	got := rows[0]
	if got.Kind != "file" || got.Text != "export.csv" || string(got.Detail) != `{"caption": "Q3"}` || got.InReplyTo != nil {
		t.Errorf("claimed %+v (detail %s)", got, got.Detail)
	}
	for call, want := range map[string]bool{"call-1": true, "call-2": true, "call-3": false} {
		if got, err := s.FileReplyExists(t.Context(), db.FileReplyExistsParams{RunID: r.ID, CallID: call}); err != nil || got != want {
			t.Errorf("FileReplyExists(%s) = %v, %v; want %v", call, got, err, want)
		}
	}
	f, err := s.GetReplyFile(t.Context(), got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if f.TeamID != c.team || f.ObjectKey != file.ObjectKey || f.Filename != "export.csv" || f.MediaType != "text/csv" || f.SizeBytes != file.SizeBytes {
		t.Errorf("file %+v", f)
	}
}

// A file sent while the run works goes out before its answer: replies to a
// conversation go in the order they were written.
func TestAFileGoesOutBeforeTheAnswer(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "file-first")
	file := c.sentFile(t, s, "m-file", "teams/x/objects/first")
	r := c.working(t, s, "m-1")
	sendFile(t, s, r.ID, file.ID, "call-1", nil)
	endChannelRun(t, s, r.ID, "succeeded", "Here it is.", "")

	first := claimReplies(t, s.Queries, 60)
	if len(first) != 1 || first[0].Kind != "file" {
		t.Fatalf("claimed %+v first, want the file", first)
	}
	if _, err := s.ReplySent(t.Context(), db.ReplySentParams{ID: first[0].ID, Attempts: first[0].Attempts}); err != nil {
		t.Fatal(err)
	}
	if next := claimReplies(t, s.Queries, 60); len(next) != 1 || next[0].Kind != "answer" {
		t.Errorf("claimed %+v next, want the answer", next)
	}
}

// What cannot be posted, or is not the conversation's, writes no reply.
func TestAFileReplyIsWrittenOnlyWhereItCanGo(t *testing.T) {
	t.Parallel()

	s := queryStore(t)

	t.Run("another conversation's file", func(t *testing.T) {
		c := newConversation(t, s, "file-other")
		other := c.elsewhere(t, s, "D-other")
		file := other.sentFile(t, s, "m-other", "teams/x/objects/other")
		r := c.working(t, s, "m-1")
		if n := sendFile(t, s, r.ID, file.ID, "call-1", nil); n != 0 {
			t.Errorf("wrote %d replies for another conversation's file", n)
		}
	})
	t.Run("a custom channel with no reply URL", func(t *testing.T) {
		c := newConversation(t, s, "file-nourl")
		file := c.sentFile(t, s, "m-file", "teams/x/objects/nourl")
		r := c.working(t, s, "m-1")
		if _, err := s.pool.Exec(t.Context(), `UPDATE channels SET reply_url = NULL WHERE id = $1`, c.channel.ID); err != nil {
			t.Fatal(err)
		}
		if n := sendFile(t, s, r.ID, file.ID, "call-1", nil); n != 0 {
			t.Errorf("wrote %d replies with nowhere to post them", n)
		}
	})
	t.Run("a run that answers no channel", func(t *testing.T) {
		c := newConversation(t, s, "file-noreply")
		file := c.sentFile(t, s, "m-file", "teams/x/objects/noreply")
		r := c.working(t, s, "m-1")
		if _, err := s.pool.Exec(t.Context(), `UPDATE runs SET reply_to_channel = false WHERE id = $1`, r.ID); err != nil {
			t.Fatal(err)
		}
		if n := sendFile(t, s, r.ID, file.ID, "call-1", nil); n != 0 {
			t.Errorf("wrote %d replies for a run that answers no channel", n)
		}
	})
}

// A file deleted before it is sent takes its reply with it: there is
// nothing left to send.
func TestDeletingAFileDeletesTheReplySendingIt(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "file-gone")
	file := c.sentFile(t, s, "m-file", "teams/x/objects/gone")
	r := c.working(t, s, "m-1")
	sendFile(t, s, r.ID, file.ID, "call-1", nil)
	if _, err := s.pool.Exec(t.Context(), `DELETE FROM attachments WHERE id = $1`, file.ID); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int64](t, s, `SELECT count(*) FROM replies WHERE run_id = $1`, r.ID); n != 0 {
		t.Errorf("%d replies left for a deleted file", n)
	}
}

// A file reply names its file and its call, the file of its own session;
// any other kind names neither. Each refusal is by the constraint that
// means it.
func TestAFileReplyNamesItsFileAndCall(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "file-shape")
	file := c.sentFile(t, s, "m-file", "teams/x/objects/shape")
	other := c.elsewhere(t, s, "D-shape-other")
	otherFile := other.sentFile(t, s, "m-other", "teams/x/objects/shape-other")
	r := c.working(t, s, "m-1")

	insert := `INSERT INTO replies (team_id, run_id, session_id, channel_id, kind, text, attachment_id, call_id)
	           VALUES ($1, $2, $3, $4, $5, 'x', $6, $7)`
	for name, tc := range map[string]struct {
		kind string
		file *uuid.UUID
		call *string
		want string
	}{
		"a file with no attachment":  {"file", nil, ptrTo("c"), "replies_file_names_its_file"},
		"a file with no call":        {"file", &file.ID, nil, "replies_file_names_its_file"},
		"a file with an empty call":  {"file", &file.ID, ptrTo(""), "replies_file_names_its_file"},
		"an answer naming a file":    {"answer", &file.ID, nil, "replies_file_names_its_file"},
		"an apology naming a call":   {"apology", nil, ptrTo("c"), "replies_file_names_its_file"},
		"another session's file":     {"file", &otherFile.ID, ptrTo("c"), "replies_file_in_session"},
		"a call id longer than 255":  {"file", &file.ID, ptrTo(strings.Repeat("c", 256)), "replies_call_id_bounded"},
		"a kind there is no such of": {"photo", nil, nil, "replies_kind_known"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.pool.Exec(t.Context(), insert, c.team, r.ID, c.session.ID, c.channel.ID, tc.kind, tc.file, tc.call)
			if got := constraintName(err); got != tc.want {
				t.Errorf("refused by %q (%v), want %q", got, err, tc.want)
			}
		})
	}
}

// Where a conversation's files go: the dashboard's is read on its stream;
// a channel's is posted, when it can be.
func TestWhereAConversationsFilesGo(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "file-reach")
	dash := scalar[uuid.UUID](t, s, `SELECT id FROM channels WHERE team_id = $1 AND provider = 'dashboard'`, c.team)
	onDashboard := mustEnsureSession(t, s, c.team, dash, "dash-1", "direct")
	noURL := mustCreateChannel(t, s, c.team, "custom", "file-reach-nourl")
	unposted := mustEnsureSession(t, s, c.team, noURL.ID, "D-nourl", "direct")

	for name, tc := range map[string]struct {
		session uuid.UUID
		want    db.GetSessionFileReachRow
	}{
		"the dashboard":                {onDashboard.ID, db.GetSessionFileReachRow{Streamed: true, Provider: "dashboard", Postable: true}},
		"a custom channel":             {c.session.ID, db.GetSessionFileReachRow{Provider: "custom", Postable: true}},
		"a custom channel with no URL": {unposted.ID, db.GetSessionFileReachRow{Provider: "custom"}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := s.GetSessionFileReach(t.Context(), tc.session)
			if err != nil || got != tc.want {
				t.Errorf("reach = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

// A run's files sent are its file_sent events, whoever reads them.
func TestARunCountsTheFilesItSent(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	c := newConversation(t, s, "file-count")
	file := c.sentFile(t, s, "m-file", "teams/x/objects/export")
	r := c.working(t, s, "m-1")
	other := c.working(t, s, "m-2")
	// c1 has an event alone, c2 two events and its reply, c3 its reply alone,
	// as a channel file whose event was never written: three calls.
	for i, e := range []struct {
		run  uuid.UUID
		call string
	}{{r.ID, "c1"}, {r.ID, "c2"}, {r.ID, "c2"}, {other.ID, "c4"}} {
		if _, err := s.pool.Exec(t.Context(),
			`INSERT INTO run_events (run_id, seq, kind, payload) VALUES ($1, $2, 'file_sent', jsonb_build_object('tool_call_id', $3::text))`,
			e.run, 100+i, e.call); err != nil {
			t.Fatal(err)
		}
	}
	for _, call := range []string{"c2", "c3"} {
		if n := sendFile(t, s, r.ID, file.ID, call, nil); n != 1 {
			t.Fatalf("reply for %s: wrote %d", call, n)
		}
	}
	if n, err := s.CountRunFilesSent(t.Context(), r.ID); err != nil || n != 3 {
		t.Errorf("CountRunFilesSent = %d, %v; want the run's 3 calls", n, err)
	}
}
