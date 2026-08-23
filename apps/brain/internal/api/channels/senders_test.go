package channels

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/api"
	"github.com/LaplacianAI/openarity/apps/brain/internal/auth"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// --- the sender half of the fake store ---

type senderState struct {
	pending  []db.PendingSender
	approved []db.ChannelSender
	members  map[uuid.UUID]string

	approvals []approval
	removals  []approval

	pendingErr  error
	approvedErr error
	memberErr   error
	approveErr  error
	removeErr   error
}

type approval struct {
	channelID uuid.UUID
	senderRef string
	userID    uuid.UUID
}

func (f *fakeStore) ListPendingSenders(_ context.Context, arg db.ListPendingSendersParams) ([]db.PendingSender, error) {
	if f.senders.pendingErr != nil {
		return nil, f.senders.pendingErr
	}
	var out []db.PendingSender
	for _, p := range f.senders.pending {
		if p.ChannelID == arg.ChannelID {
			out = append(out, p)
		}
	}
	if n := int(arg.PageSize); n < len(out) {
		out = out[:n]
	}
	return out, nil
}

func (f *fakeStore) ListChannelSenders(_ context.Context, arg db.ListChannelSendersParams) ([]db.ChannelSender, error) {
	if f.senders.approvedErr != nil {
		return nil, f.senders.approvedErr
	}
	var out []db.ChannelSender
	for _, s := range f.senders.approved {
		if s.ChannelID == arg.ChannelID {
			out = append(out, s)
		}
	}
	if n := int(arg.PageSize); n < len(out) {
		out = out[:n]
	}
	return out, nil
}

func (f *fakeStore) FindTeamMember(_ context.Context, arg db.FindTeamMemberParams) (string, error) {
	if f.senders.memberErr != nil {
		return "", f.senders.memberErr
	}
	if role, ok := f.senders.members[arg.UserID]; ok {
		return role, nil
	}
	return "", pgx.ErrNoRows
}

func (f *fakeStore) ApproveSender(_ context.Context, arg db.ApproveSenderParams) error {
	f.senders.approvals = append(f.senders.approvals,
		approval{channelID: arg.ChannelID, senderRef: arg.SenderRef, userID: arg.UserID})
	return f.senders.approveErr
}

func (f *fakeStore) RemoveSender(_ context.Context, arg db.RemoveSenderParams) error {
	f.senders.removals = append(f.senders.removals,
		approval{channelID: arg.ChannelID, senderRef: arg.SenderRef})
	return f.senders.removeErr
}

// --- the harness ---

// senderFixture is a team with one channel, one pending sender and one member
// who could be approved — the state every test below starts from.
type senderFixture struct {
	teamID    uuid.UUID
	channelID uuid.UUID
	userID    uuid.UUID
	store     *fakeStore
	authz     *fakeAuthz
	caller    *auth.User
}

func newSenderFixture(t *testing.T) *senderFixture {
	t.Helper()

	f := &senderFixture{teamID: uuid.New(), channelID: uuid.New(), userID: uuid.New()}
	f.store = &fakeStore{
		channels: []db.Channel{{
			ID: f.channelID, TeamID: f.teamID, Provider: "custom", Name: "support",
		}},
		senders: senderState{
			pending: []db.PendingSender{{
				ChannelID:  f.channelID,
				SenderRef:  "U01ABC",
				SenderName: "Asha Menon",
				FirstSeen:  time.Unix(1755412345, 0).UTC(),
				LastSeen:   time.Unix(1755412999, 0).UTC(),
				SeenCount:  3,
			}},
			members: map[uuid.UUID]string{f.userID: "member"},
		},
	}
	f.authz = &fakeAuthz{allowed: true}
	f.caller = memberOf(f.teamID, "admin")
	return f
}

func (f *senderFixture) base() string {
	return "/teams/" + f.teamID.String() + "/channels/" + f.channelID.String() + "/senders"
}

func (f *senderFixture) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return call(t, f.store, newFakeSecrets(), f.authz, f.caller, method, path, body)
}

func (f *senderFixture) approve(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	return f.do(t, http.MethodPost, f.base(), body)
}

func (f *senderFixture) approveUser(t *testing.T, ref string, userID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	return f.approve(t, `{"sender_ref":"`+ref+`","user_id":"`+userID.String()+`"}`)
}

func pendingPage(t *testing.T, rec *httptest.ResponseRecorder) api.Page[pendingSender] {
	t.Helper()

	var got api.Page[pendingSender]
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not a page of pending senders: %v (%s)", err, rec.Body)
	}
	return got
}

func sendersPage(t *testing.T, rec *httptest.ResponseRecorder) api.Page[channelSender] {
	t.Helper()

	var got api.Page[channelSender]
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not a page of senders: %v (%s)", err, rec.Body)
	}
	return got
}

// --- authorisation ---

// Approving a stranger grants them the right to instruct an agent as a named
// user. That is the same kind of act as connecting the channel, so it carries
// the same permission — and the handler contains no check, because the guard
// runs first.
func TestEverySenderRouteNeedsChannelWrite(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		method  string
		suffix  string
		body    string
		granted int
	}{
		"list pending":  {http.MethodGet, "/pending", "", http.StatusOK},
		"list approved": {http.MethodGet, "", "", http.StatusOK},
		"approve":       {http.MethodPost, "", "", http.StatusBadRequest},
		"remove":        {http.MethodDelete, "?ref=U01ABC", "", http.StatusNoContent},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			refused := newSenderFixture(t)
			refused.authz.allowed = false
			if rec := refused.do(t, tc.method, refused.base()+tc.suffix, tc.body); rec.Code != http.StatusForbidden {
				t.Errorf("%s without channel:write = %d, want 403", name, rec.Code)
			}

			// And the same request with the permission does not 403, so the
			// test above is about the permission rather than about the route
			// being broken.
			granted := newSenderFixture(t)
			if rec := granted.do(t, tc.method, granted.base()+tc.suffix, tc.body); rec.Code == http.StatusForbidden {
				t.Errorf("%s with channel:write = 403", name)
			}
		})
	}
}

// These routes are team-scoped, so a refusal comes from Can rather than from a
// membership check — and a stranger to a real team is refused exactly as
// anyone naming a team that does not exist. Both are 403 with the same body,
// so neither answer says whether the id is real.
//
// That differs from the member-scoped GET /teams/{id}/channels, which answers
// 404 for both. Either is fine; answering differently for the two would not
// be.
func TestARefusalDoesNotSayWhetherTheTeamExists(t *testing.T) {
	t.Parallel()

	stranger := newSenderFixture(t)
	stranger.authz.allowed = false
	stranger.caller = outsider()
	real := stranger.do(t, http.MethodGet, stranger.base()+"/pending", "")

	imaginary := newSenderFixture(t)
	imaginary.authz.allowed = false
	imaginary.caller = outsider()
	path := "/teams/" + uuid.New().String() + "/channels/" + uuid.New().String() + "/senders/pending"
	invented := imaginary.do(t, http.MethodGet, path, "")

	if real.Code != invented.Code || real.Body.String() != invented.Body.String() {
		t.Errorf("a real team answered %d %q and an invented one %d %q",
			real.Code, real.Body, invented.Code, invented.Body)
	}
	if real.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", real.Code)
	}
}

// The guard authorised the caller for the team in the path, not for this
// channel. Without the cross-team check an admin of any team could read and
// approve senders on any channel by naming their own team in the URL.
func TestAChannelInAnotherTeamIsNotFound(t *testing.T) {
	t.Parallel()

	for name, method := range map[string]string{
		"list pending":  http.MethodGet,
		"list approved": "GET-approved",
		"approve":       http.MethodPost,
		"remove":        http.MethodDelete,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newSenderFixture(t)
			// The channel exists, but it belongs to somebody else.
			f.store.channels[0].TeamID = uuid.New()

			path := f.base()
			body := ""
			switch method {
			case http.MethodPost:
				body = `{"sender_ref":"U01ABC","user_id":"` + f.userID.String() + `"}`
			case http.MethodDelete:
				path += "?ref=U01ABC"
			case http.MethodGet:
				path += "/pending"
			case "GET-approved":
				method = http.MethodGet
			}

			if rec := f.do(t, method, path, body); rec.Code != http.StatusNotFound {
				t.Errorf("%s on another team's channel = %d, want 404", name, rec.Code)
			}
			if n := len(f.store.senders.approvals) + len(f.store.senders.removals); n != 0 {
				t.Errorf("the write went through anyway (%d calls)", n)
			}
		})
	}
}

func TestAChannelThatDoesNotExistIsNotFound(t *testing.T) {
	t.Parallel()

	f := newSenderFixture(t)
	path := "/teams/" + f.teamID.String() + "/channels/" + uuid.New().String() + "/senders/pending"

	if rec := f.do(t, http.MethodGet, path, ""); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestAMalformedChannelIDIsRefused(t *testing.T) {
	t.Parallel()

	f := newSenderFixture(t)
	path := "/teams/" + f.teamID.String() + "/channels/not-a-uuid/senders/pending"

	if rec := f.do(t, http.MethodGet, path, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// --- the pending queue ---

func TestPendingSendersComeBackAsAPage(t *testing.T) {
	t.Parallel()

	f := newSenderFixture(t)

	rec := f.do(t, http.MethodGet, f.base()+"/pending", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	page := pendingPage(t, rec)
	if len(page.Items) != 1 {
		t.Fatalf("%d pending senders, want 1", len(page.Items))
	}

	got := page.Items[0]
	switch {
	case got.SenderRef != "U01ABC":
		t.Errorf("SenderRef = %q", got.SenderRef)
	case got.SenderName != "Asha Menon":
		t.Errorf("SenderName = %q", got.SenderName)
	case got.SeenCount != 3:
		t.Errorf("SeenCount = %d, want 3", got.SeenCount)
	case got.FirstSeen.IsZero() || got.LastSeen.IsZero():
		t.Errorf("timestamps = %v / %v", got.FirstSeen, got.LastSeen)
	}
}

// jq '.items | length' fails on null, so the slice is never nil.
func TestAnEmptyQueueReturnsAnEmptyArray(t *testing.T) {
	t.Parallel()

	f := newSenderFixture(t)
	f.store.senders.pending = nil

	body := f.do(t, http.MethodGet, f.base()+"/pending", "").Body.String()
	if strings.Contains(body, `"items":null`) {
		t.Errorf("body %q has a null items array", body)
	}
}

// A display name is typed by the person speaking, on every platform. It is
// returned as data — JSON-encoded, never interpolated — so a name full of
// quotes and control characters produces a body that still parses.
func TestAHostileDisplayNameIsReturnedAsData(t *testing.T) {
	t.Parallel()

	hostile := `","admin":true,"x":"` + "\t" + `<script>`

	f := newSenderFixture(t)
	f.store.senders.pending[0].SenderName = hostile

	rec := f.do(t, http.MethodGet, f.base()+"/pending", "")
	page := pendingPage(t, rec)

	if len(page.Items) != 1 {
		t.Fatalf("the body did not survive a hostile name: %s", rec.Body)
	}
	if page.Items[0].SenderName != hostile {
		t.Errorf("SenderName = %q, want it round-tripped unchanged", page.Items[0].SenderName)
	}
}

func TestApprovedSendersComeBackAsAPage(t *testing.T) {
	t.Parallel()

	f := newSenderFixture(t)
	f.store.senders.approved = []db.ChannelSender{{
		ChannelID: f.channelID, SenderRef: "U01ABC", UserID: f.userID,
		CreatedAt: time.Unix(1755412345, 0).UTC(),
	}}

	rec := f.do(t, http.MethodGet, f.base(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}

	page := sendersPage(t, rec)
	if len(page.Items) != 1 {
		t.Fatalf("%d senders, want 1", len(page.Items))
	}
	if page.Items[0].UserID != f.userID {
		t.Errorf("UserID = %s, want %s", page.Items[0].UserID, f.userID)
	}
}

func TestAListFailureIsReported(t *testing.T) {
	t.Parallel()

	f := newSenderFixture(t)
	f.store.senders.pendingErr = errAny

	if rec := f.do(t, http.MethodGet, f.base()+"/pending", ""); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// --- approving ---

func TestApprovingLinksTheSenderToTheUser(t *testing.T) {
	t.Parallel()

	f := newSenderFixture(t)

	if rec := f.approveUser(t, "U01ABC", f.userID); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body)
	}

	if len(f.store.senders.approvals) != 1 {
		t.Fatalf("made %d approvals, want 1", len(f.store.senders.approvals))
	}
	got := f.store.senders.approvals[0]
	switch {
	case got.channelID != f.channelID:
		t.Errorf("channelID = %s, want %s", got.channelID, f.channelID)
	case got.senderRef != "U01ABC":
		t.Errorf("senderRef = %q", got.senderRef)
	case got.userID != f.userID:
		t.Errorf("userID = %s, want %s", got.userID, f.userID)
	}
}

// Approving grants a voice in this team. Somebody outside it must not be
// nameable, or approval becomes a way to add a member without membership:write.
func TestApprovingRejectsAUserOutsideTheTeam(t *testing.T) {
	t.Parallel()

	f := newSenderFixture(t)
	stranger := uuid.New()

	if rec := f.approveUser(t, "U01ABC", stranger); rec.Code != http.StatusBadRequest {
		t.Errorf("approving an outsider = %d, want 400", rec.Code)
	}
	if len(f.store.senders.approvals) != 0 {
		t.Error("the approval went through anyway")
	}
}

func TestApprovingRejectsAnIncompleteBody(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"no sender_ref":    `{"user_id":"11111111-1111-1111-1111-111111111111"}`,
		"empty sender_ref": `{"sender_ref":"","user_id":"11111111-1111-1111-1111-111111111111"}`,
		"blank sender_ref": `{"sender_ref":"   ","user_id":"11111111-1111-1111-1111-111111111111"}`,
		"no user_id":       `{"sender_ref":"U01ABC"}`,
		"zero user_id":     `{"sender_ref":"U01ABC","user_id":"00000000-0000-0000-0000-000000000000"}`,
		"not an object":    `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newSenderFixture(t)
			rec := f.approve(t, body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s = %d, want 400", name, rec.Code)
			}
			if len(f.store.senders.approvals) != 0 {
				t.Errorf("%s was written anyway", name)
			}

			// A missing field must not be reported as a membership problem.
			// Dropping the explicit check still gives 400 — the membership
			// lookup finds nothing for the zero uuid — so only the message
			// distinguishes "you forgot it" from "they are not in the team",
			// and that is the whole reason the check is there.
			if strings.Contains(name, "user_id") &&
				strings.Contains(rec.Body.String(), "not in this team") {
				t.Errorf("%s was reported as %q", name, strings.TrimSpace(rec.Body.String()))
			}
		})
	}
}

// The same bound the gateway refuses at. An admin cannot put a row into the
// table that the column would reject, and the failure is theirs to read
// rather than a 500.
func TestApprovingRejectsAnOversizedRef(t *testing.T) {
	t.Parallel()

	f := newSenderFixture(t)
	huge := strings.Repeat("u", 257)

	if rec := f.approveUser(t, huge, f.userID); rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if len(f.store.senders.approvals) != 0 {
		t.Error("an oversized ref was written")
	}
}

// An admin who already knows a provider id may link it before its owner has
// ever spoken, which is how a channel is set up without a stranger's message
// sitting in a queue first.
func TestApprovingARefThatWasNeverPendingIsAllowed(t *testing.T) {
	t.Parallel()

	f := newSenderFixture(t)

	if rec := f.approveUser(t, "U-NEVER-SEEN", f.userID); rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204: %s", rec.Code, rec.Body)
	}
}

func TestAFailedApprovalIsReported(t *testing.T) {
	t.Parallel()

	f := newSenderFixture(t)
	f.store.senders.approveErr = errAny

	if rec := f.approveUser(t, "U01ABC", f.userID); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// The membership lookup failing is not the same as the user not being a
// member: one is our database, the other is the caller's mistake.
func TestAFailedMembershipLookupIsNotABadRequest(t *testing.T) {
	t.Parallel()

	f := newSenderFixture(t)
	f.store.senders.memberErr = errAny

	if rec := f.approveUser(t, "U01ABC", f.userID); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// --- removing ---

func TestRemovingUnlinksTheSender(t *testing.T) {
	t.Parallel()

	f := newSenderFixture(t)

	if rec := f.do(t, http.MethodDelete, f.base()+"?ref=U01ABC", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body)
	}
	if len(f.store.senders.removals) != 1 {
		t.Fatalf("made %d removals, want 1", len(f.store.senders.removals))
	}
	if got := f.store.senders.removals[0]; got.senderRef != "U01ABC" || got.channelID != f.channelID {
		t.Errorf("removed %+v", got)
	}
}

// The ref is a query parameter and not a path segment because a ref is
// provider-controlled text. url.PathEscape leaves dots alone and the mux
// redirects "." and ".." before any handler runs, so a sender with one of
// those refs could never be removed.
func TestARefThePathCouldNotCarryStillWorks(t *testing.T) {
	t.Parallel()

	for name, ref := range map[string]string{
		"a slash":      "team/alice",
		"dot dot":      "..",
		"a single dot": ".",
		"a space":      "Asha Menon",
		"a percent":    "100%",
		"a hash":       "a#b",
		"an ampersand": "a&ref=b",
		"non-ascii":    "señor",
		"slack style":  "C123:1699999999.000100",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newSenderFixture(t)
			path := f.base() + "?ref=" + url.QueryEscape(ref)

			if rec := f.do(t, http.MethodDelete, path, ""); rec.Code != http.StatusNoContent {
				t.Fatalf("removing %q = %d, want 204", ref, rec.Code)
			}
			if len(f.store.senders.removals) != 1 {
				t.Fatalf("made %d removals for %q", len(f.store.senders.removals), ref)
			}
			if got := f.store.senders.removals[0].senderRef; got != ref {
				t.Errorf("removed %q, want %q — the ref was mangled in transit", got, ref)
			}
		})
	}
}

func TestRemovingNeedsARef(t *testing.T) {
	t.Parallel()

	for name, query := range map[string]string{
		"no ref at all": "",
		"an empty ref":  "?ref=",
		"a blank ref":   "?ref=%20%20",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newSenderFixture(t)
			if rec := f.do(t, http.MethodDelete, f.base()+query, ""); rec.Code != http.StatusBadRequest {
				t.Errorf("%s = %d, want 400", name, rec.Code)
			}
			if len(f.store.senders.removals) != 0 {
				t.Errorf("%s removed something", name)
			}
		})
	}
}

// Removing somebody who was never linked is success, not 404. The caller
// asked for a state and that state holds — and a 404 would let an admin probe
// which provider ids are linked one request at a time.
func TestRemovingSomebodyWhoWasNeverLinkedIsSuccess(t *testing.T) {
	t.Parallel()

	f := newSenderFixture(t)

	if rec := f.do(t, http.MethodDelete, f.base()+"?ref=U-NEVER-SEEN", ""); rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}

func TestAFailedRemovalIsReported(t *testing.T) {
	t.Parallel()

	f := newSenderFixture(t)
	f.store.senders.removeErr = errAny

	if rec := f.do(t, http.MethodDelete, f.base()+"?ref=U01ABC", ""); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// --- the secret still never leaks ---

// Every route in this package is checked for it, including the ones added
// after the rule was written.
func TestNoSenderResponseCarriesASecret(t *testing.T) {
	t.Parallel()

	f := newSenderFixture(t)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, f.base() + "/pending", ""},
		{http.MethodGet, f.base(), ""},
		{http.MethodPost, f.base(), `{"sender_ref":"U01ABC","user_id":"` + f.userID.String() + `"}`},
		{http.MethodDelete, f.base() + "?ref=U01ABC", ""},
	} {
		rec := f.do(t, tc.method, tc.path, tc.body)
		if strings.Contains(rec.Body.String(), secretPrefix) {
			t.Errorf("%s %s answered with something that looks like a secret: %s",
				tc.method, tc.path, rec.Body)
		}
	}
}

// --- paging ---

func TestPendingSendersPageFromACursor(t *testing.T) {
	t.Parallel()

	f := newSenderFixture(t)
	f.store.senders.pending = nil
	for i, ref := range []string{"u-1", "u-2", "u-3"} {
		f.store.senders.pending = append(f.store.senders.pending, db.PendingSender{
			ChannelID: f.channelID, SenderRef: ref, SenderName: ref,
			FirstSeen: time.Unix(int64(1755412345-i*60), 0).UTC(),
			SeenCount: 1,
		})
	}

	first := pendingPage(t, f.do(t, http.MethodGet, f.base()+"/pending?limit=2", ""))
	if len(first.Items) != 2 {
		t.Fatalf("%d items on the first page, want 2", len(first.Items))
	}
	if first.NextCursor == nil {
		t.Fatal("no cursor when a page was trimmed")
	}

	// The cursor has to survive the round trip and reach the query, or paging
	// silently restarts from the top and a caller loops forever.
	f.store.senders.pending = nil
	rec := f.do(t, http.MethodGet, f.base()+"/pending?limit=2&cursor="+url.QueryEscape(*first.NextCursor), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
}

func TestApprovedSendersPageFromACursor(t *testing.T) {
	t.Parallel()

	f := newSenderFixture(t)
	for i, ref := range []string{"u-1", "u-2", "u-3"} {
		f.store.senders.approved = append(f.store.senders.approved, db.ChannelSender{
			ChannelID: f.channelID, SenderRef: ref, UserID: f.userID,
			CreatedAt: time.Unix(int64(1755412345-i*60), 0).UTC(),
		})
	}

	first := sendersPage(t, f.do(t, http.MethodGet, f.base()+"?limit=2", ""))
	if len(first.Items) != 2 || first.NextCursor == nil {
		t.Fatalf("first page = %d items, cursor %v", len(first.Items), first.NextCursor)
	}

	rec := f.do(t, http.MethodGet, f.base()+"?limit=2&cursor="+url.QueryEscape(*first.NextCursor), "")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
}

// A cursor is opaque to the caller, so a mangled one is their mistake to read
// rather than a 500 that says nothing.
func TestAMangledCursorIsRefused(t *testing.T) {
	t.Parallel()

	for name, path := range map[string]string{
		"pending":  "/pending?cursor=not-base64",
		"approved": "?cursor=not-base64",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newSenderFixture(t)
			if rec := f.do(t, http.MethodGet, f.base()+path, ""); rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestAnUnreadableLimitIsRefused(t *testing.T) {
	t.Parallel()

	for name, path := range map[string]string{
		"pending":  "/pending?limit=lots",
		"approved": "?limit=lots",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newSenderFixture(t)
			if rec := f.do(t, http.MethodGet, f.base()+path, ""); rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

// Reading the channel failing is our problem, and it is not the same as the
// channel being gone: a 404 would tell a caller their id is wrong when the
// database is merely unreachable.
func TestAFailedChannelLookupIsNotAFourOhFour(t *testing.T) {
	t.Parallel()

	f := newSenderFixture(t)
	f.store.err = errAny

	if rec := f.do(t, http.MethodGet, f.base()+"/pending", ""); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestAFailedApprovedListIsReported(t *testing.T) {
	t.Parallel()

	f := newSenderFixture(t)
	f.store.senders.approvedErr = errAny

	if rec := f.do(t, http.MethodGet, f.base(), ""); rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// --- the branches that cannot be reached ---

// MapPage only fails when a cursor will not marshal, and these two are plain
// structs of a time and a string. The handlers still check the error, because
// deleting error handling to raise a coverage number is how the one case that
// could fail stops being handled.
//
// This is the assumption those unreachable branches rest on. If somebody puts
// a channel, a func or a NaN in a cursor, this fails rather than the branch
// quietly becoming live.
func TestEveryCursorInThisPackageCanBeEncoded(t *testing.T) {
	t.Parallel()

	for name, cursor := range map[string]any{
		"pending":  pendingCursor{FirstSeen: time.Now(), Ref: "u-1"},
		"approved": senderCursor{CreatedAt: time.Now(), Ref: "u-1"},
		"channel":  channelCursor{CreatedAt: time.Now(), ID: uuid.New()},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := api.EncodeCursor(cursor); err != nil {
				t.Errorf("a %s cursor no longer encodes: %v — the handler branch that "+
					"handles this is now reachable and needs a test", name, err)
			}
		})
	}
}
