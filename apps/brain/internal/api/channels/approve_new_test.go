package channels

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

func (f *senderFixture) approveNew(t *testing.T, ref, role, name string) int {
	t.Helper()
	body := `{"sender_ref":"` + ref + `","new":{"role":"` + role + `","name":"` + name + `"}}`
	return f.approve(t, body).Code
}

func TestApprovingSomeoneNewAsksTheStoreForTheRoleAndName(t *testing.T) {
	t.Parallel()
	f := newSenderFixture(t)
	f.asRole("approver")

	if code := f.approveNew(t, "U01ABC", " guest ", "Ravi"); code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", code)
	}
	got := f.store.senders.newApprovals
	if len(got) != 1 || got[0].role != "guest" || got[0].name != "Ravi" || got[0].channelID != f.channelID || got[0].expected != nil {
		t.Fatalf("new approvals = %+v; want one guest named Ravi, on an unbound ref", got)
	}
	if len(f.store.senders.approvals) != 0 {
		t.Error("approving someone new also bound the sender to a member")
	}
}

// Someone new is named by the approver, else by the name they gave while
// waiting, else by their ref: never nameless, never a name with controls in.
func TestSomeoneNewIsAlwaysNamed(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ ref, asked, want string }{
		"the approver's":         {"U01ABC", "Ravi", "Ravi"},
		"the waiting sender's":   {"U01ABC", "", "Asha Menon"},
		"the ref, with no other": {"U09NEVER", "", "U09NEVER"},
		"cleaned of controls":    {"U01ABC", `Ra\u0000vi`, "Ra vi"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newSenderFixture(t)
			if code := f.approveNew(t, tc.ref, "guest", tc.asked); code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204", code)
			}
			if got := f.store.senders.newApprovals[0].name; got != tc.want {
				t.Errorf("name = %q, want %q", got, tc.want)
			}
		})
	}
}

// The approver's ceiling holds for someone new as for a member: no role
// that holds what the approver does not.
func TestSomeoneNewGetsOnlyARoleTheCallerCovers(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		caller, role string
		want         int
	}{
		"an approver, a guest":  {"approver", "guest", http.StatusNoContent},
		"an approver, a member": {"approver", "member", http.StatusNoContent},
		"an approver, an admin": {"approver", "admin", http.StatusForbidden},
		"an admin, an admin":    {"admin", "admin", http.StatusNoContent},
		"a role nobody defined": {"admin", "owner", http.StatusBadRequest},
		"no role at all":        {"admin", " ", http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newSenderFixture(t)
			f.asRole(tc.caller)
			if code := f.approveNew(t, "U01ABC", tc.role, "Ravi"); code != tc.want {
				t.Fatalf("status = %d, want %d", code, tc.want)
			}
			if wrote := len(f.store.senders.newApprovals); (tc.want == http.StatusNoContent) != (wrote == 1) {
				t.Errorf("%d approvals written for a %d", wrote, tc.want)
			}
		})
	}
}

// Taking a sender from a member for someone new takes it from that member,
// so the caller must cover them, and the write names them.
func TestSomeoneNewTakesASenderOnlyFromSomeoneTheCallerCovers(t *testing.T) {
	t.Parallel()

	t.Run("an admin's sender, by an approver", func(t *testing.T) {
		t.Parallel()
		f := newSenderFixture(t)
		_, _, admin := f.asRole("approver")
		f.store.senders.bound = map[string]uuid.UUID{"U01ABC": admin}
		if code := f.approveNew(t, "U01ABC", "guest", "Ravi"); code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", code)
		}
		if len(f.store.senders.newApprovals) != 0 {
			t.Error("the admin's sender was taken")
		}
	})

	t.Run("a member's sender, by an approver", func(t *testing.T) {
		t.Parallel()
		f := newSenderFixture(t)
		member, _, _ := f.asRole("approver")
		f.store.senders.bound = map[string]uuid.UUID{"U01ABC": member}
		if code := f.approveNew(t, "U01ABC", "guest", "Ravi"); code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", code)
		}
		if got := f.store.senders.newApprovals[0].expected; got == nil || *got != member {
			t.Errorf("the write names %v as the holder, want the member it checked", got)
		}
	})
}

func TestSomeoneNewIsRefusedForTheBody(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"both a user and new": `{"sender_ref":"U01ABC","user_id":"` + uuid.NewString() + `","new":{"role":"guest"}}`,
		"neither":             `{"sender_ref":"U01ABC"}`,
		"no ref":              `{"new":{"role":"guest"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newSenderFixture(t)
			if code := f.approve(t, body).Code; code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", code)
			}
			if len(f.store.senders.newApprovals)+len(f.store.senders.approvals) != 0 {
				t.Error("a refused body was written")
			}
		})
	}
}

func TestSomeoneNewWhoseBindingMovedIsAConflict(t *testing.T) {
	t.Parallel()
	f := newSenderFixture(t)
	f.store.senders.moved = true
	if code := f.approveNew(t, "U01ABC", "guest", "Ravi"); code != http.StatusConflict {
		t.Errorf("status = %d, want 409", code)
	}
}

func TestAFailedNewApprovalIsA500ThatLeaksNothing(t *testing.T) {
	t.Parallel()
	f := newSenderFixture(t)
	f.store.senders.approveErr = errors.New("pq: password=hunter2 connection reset")
	rec := f.approve(t, `{"sender_ref":"U01ABC","new":{"role":"guest","name":"Ravi"}}`)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "hunter2") {
		t.Errorf("status %d, body %q; want a 500 that says nothing of the cause", rec.Code, rec.Body)
	}
}

func TestTheApprovedListSaysWhoEachSenderIs(t *testing.T) {
	t.Parallel()
	f := newSenderFixture(t)
	f.store.senders.approved = append(f.store.senders.approved, approvedAs(f.channelID, "U01ABC", f.userID))

	page := sendersPage(t, f.do(t, http.MethodGet, f.base(), ""))
	if len(page.Items) != 1 || page.Items[0].Role != "member" {
		t.Errorf("items = %+v; want the sender with its user's role", page.Items)
	}
}

func approvedAs(channelID uuid.UUID, ref string, userID uuid.UUID) db.ChannelSender {
	return db.ChannelSender{ChannelID: channelID, SenderRef: ref, UserID: userID}
}
