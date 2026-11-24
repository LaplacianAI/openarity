package store

import (
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// seedRun is a run in session, of the session's team.
func seedRun(t *testing.T, s *Store, session uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := s.pool.Exec(t.Context(), `
		INSERT INTO runs (id, team_id, session_id, root_run_id, agent_name, input, reply_to_channel)
		SELECT $1, team_id, id, $1, 'x', 'hi', false FROM sessions WHERE id = $2`, id, session); err != nil {
		t.Fatalf("insert run: %v", err)
	}
	return id
}

// toolFile is a file run's tool returned, kept in session.
func toolFile(t *testing.T, s *Store, session, run uuid.UUID, key string) db.CreateToolAttachmentParams {
	t.Helper()
	return db.CreateToolAttachmentParams{
		SessionID: session,
		TeamID:    scalar[uuid.UUID](t, s, `SELECT team_id FROM sessions WHERE id = $1`, session),
		RunID:     &run, Tool: ptrTo("mail__fetch"), CallID: ptrTo("call-1"),
		ObjectKey: key, MediaType: "application/zip", SizeBytes: 12, Filename: "bundle.zip",
	}
}

// A tool's file is a conversation attachment with the run's call as its
// source and no message, listed for the runtime with a person's files and
// left out of a list of people's.
func TestAToolsFileIsKeptWithItsCall(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	messageID, session, _ := seedMessage(t, s, "tool-file")
	person := mustCreateAttachment(t, s, attachment(t, s, messageID, session, "teams/x/objects/person"))
	run := seedRun(t, s, session)

	got, err := s.CreateToolAttachment(t.Context(), toolFile(t, s, session, run, "teams/x/objects/tool"))
	if err != nil {
		t.Fatalf("CreateToolAttachment: %v", err)
	}
	if got.MessageID != nil || got.RunID == nil || *got.RunID != run || *got.Tool != "mail__fetch" || *got.CallID != "call-1" || got.KeyVersion != 1 {
		t.Errorf("stored %+v, want the run's call as its source and no message", got)
	}
	if rows := listAttachments(t, s, session, 10); len(rows) != 2 {
		t.Errorf("listed %d for the runtime, want the person's and the tool's", len(rows))
	}
	people, err := s.ListAttachmentsBySession(t.Context(), db.ListAttachmentsBySessionParams{SessionID: session, PeopleOnly: true, PageSize: 10})
	if err != nil || len(people) != 1 || people[0].ID != person.ID {
		t.Errorf("listed %+v for people, err %v; want the person's alone", people, err)
	}
	if in, err := s.GetAttachmentInSession(t.Context(), db.GetAttachmentInSessionParams{ID: got.ID, SessionID: session}); err != nil || in.ID != got.ID {
		t.Errorf("GetAttachmentInSession = %v, want the tool's file", err)
	}
}

// A file has exactly one source, and a run's file names a run of its own
// session; each refusal is by the constraint that means it.
func TestAToolFileHasOneSourceInItsSession(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	messageID, session, _ := seedMessage(t, s, "tool-source")
	run := seedRun(t, s, session)
	_, other, _ := seedMessage(t, s, "tool-source-other")
	otherRun := seedRun(t, s, other)

	for name, tc := range map[string]struct {
		sql  string
		args []any
		want string
	}{
		"both a message and a run": {
			`INSERT INTO attachments (message_id, session_id, team_id, run_id, tool, call_id, object_key, media_type, size_bytes, filename)
			 SELECT $1, $2, team_id, $3, 't', 'c', 'k-both', 'x/y', 1, 'f' FROM sessions WHERE id = $2`,
			[]any{messageID, session, run},
			"attachments_one_source",
		},
		"neither": {
			`INSERT INTO attachments (session_id, team_id, object_key, media_type, size_bytes, filename)
			 SELECT $1, team_id, 'k-none', 'x/y', 1, 'f' FROM sessions WHERE id = $1`,
			[]any{session},
			"attachments_one_source",
		},
		"a run with a null tool": {
			`INSERT INTO attachments (session_id, team_id, run_id, call_id, object_key, media_type, size_bytes, filename)
			 SELECT $1, team_id, $2, 'c', 'k-null-tool', 'x/y', 1, 'f' FROM sessions WHERE id = $1`,
			[]any{session, run},
			"attachments_one_source",
		},
		"a run with a null call id": {
			`INSERT INTO attachments (session_id, team_id, run_id, tool, object_key, media_type, size_bytes, filename)
			 SELECT $1, team_id, $2, 't', 'k-null-call', 'x/y', 1, 'f' FROM sessions WHERE id = $1`,
			[]any{session, run},
			"attachments_one_source",
		},
		"a run with an empty tool": {
			`INSERT INTO attachments (session_id, team_id, run_id, tool, call_id, object_key, media_type, size_bytes, filename)
			 SELECT $1, team_id, $2, '', 'c', 'k-empty-tool', 'x/y', 1, 'f' FROM sessions WHERE id = $1`,
			[]any{session, run},
			"attachments_one_source",
		},
		"a run with no call": {
			`INSERT INTO attachments (session_id, team_id, run_id, tool, call_id, object_key, media_type, size_bytes, filename)
			 SELECT $1, team_id, $2, 't', '', 'k-call', 'x/y', 1, 'f' FROM sessions WHERE id = $1`,
			[]any{session, run},
			"attachments_one_source",
		},
		"another session's run": {
			`INSERT INTO attachments (session_id, team_id, run_id, tool, call_id, object_key, media_type, size_bytes, filename)
			 SELECT $1, team_id, $2, 't', 'c', 'k-other', 'x/y', 1, 'f' FROM sessions WHERE id = $1`,
			[]any{session, otherRun},
			"attachments_run_in_session",
		},
		"a tool name past 255": {
			`INSERT INTO attachments (session_id, team_id, run_id, tool, call_id, object_key, media_type, size_bytes, filename)
			 SELECT $1, team_id, $2, repeat('t', 256), 'c', 'k-long', 'x/y', 1, 'f' FROM sessions WHERE id = $1`,
			[]any{session, run},
			"attachments_tool_bounded",
		},
		"a call id past 255": {
			`INSERT INTO attachments (session_id, team_id, run_id, tool, call_id, object_key, media_type, size_bytes, filename)
			 SELECT $1, team_id, $2, 't', repeat('c', 256), 'k-long-call', 'x/y', 1, 'f' FROM sessions WHERE id = $1`,
			[]any{session, run},
			"attachments_call_id_bounded",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := s.pool.Exec(t.Context(), tc.sql, tc.args...)
			if got := constraintName(err); got != tc.want {
				t.Errorf("err = %v (constraint %q), want %s", err, got, tc.want)
			}
		})
	}
}

// Deleting a run deletes the files its tools returned, and their objects are
// owed to the reaper; a person's file in the session stays.
func TestDeletingARunTombstonesItsFiles(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	messageID, session, _ := seedMessage(t, s, "tool-delete")
	person := mustCreateAttachment(t, s, attachment(t, s, messageID, session, "teams/x/objects/stays-"+uuid.NewString()))
	run := seedRun(t, s, session)
	key := "teams/x/objects/goes-" + uuid.NewString()
	if _, err := s.CreateToolAttachment(t.Context(), toolFile(t, s, session, run, key)); err != nil {
		t.Fatalf("CreateToolAttachment: %v", err)
	}

	if _, err := s.pool.Exec(t.Context(), `DELETE FROM runs WHERE id = $1`, run); err != nil {
		t.Fatalf("deleting the run: %v", err)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM attachments WHERE run_id = $1`, run); n != 0 {
		t.Errorf("%d of the run's files survived it", n)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM deleted_objects WHERE object_key = $1`, key); n != 1 {
		t.Errorf("the run's object was tombstoned %d times, want once", n)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM attachments WHERE id = $1`, person.ID); n != 1 {
		t.Error("the person's file went with the run")
	}
}
