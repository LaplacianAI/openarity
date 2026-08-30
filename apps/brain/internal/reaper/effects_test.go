package reaper

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

type fakeObjectRows struct {
	rows       []db.DeletedObject
	references map[string]int64

	claimErr error
	countErr error

	forgot []string
}

func (f *fakeObjectRows) ClaimDeletedObjects(
	_ context.Context, arg db.ClaimDeletedObjectsParams,
) ([]db.DeletedObject, error) {
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	if int32(len(f.rows)) > arg.BatchSize {
		return f.rows[:arg.BatchSize], nil
	}
	return f.rows, nil
}

func (f *fakeObjectRows) CountAttachmentsByObjectKey(_ context.Context, key string) (int64, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	return f.references[key], nil
}

func (f *fakeObjectRows) ForgetDeletedObject(_ context.Context, key string) error {
	f.forgot = append(f.forgot, key)
	return nil
}

func (f *fakeObjectRows) DeletedObjectBacklog(context.Context) ([]db.DeletedObjectBacklogRow, error) {
	if len(f.rows) == 0 {
		return nil, nil
	}
	return []db.DeletedObjectBacklogRow{{
		Outstanding: int64(len(f.rows)), Oldest: f.rows[0].DeletedAt,
	}}, nil
}

type fakeObjectStore struct {
	err error

	deleted []string
	teams   []uuid.UUID
}

func (f *fakeObjectStore) Delete(_ context.Context, teamID uuid.UUID, key string) error {
	if f.err != nil {
		return f.err
	}
	f.deleted = append(f.deleted, key)
	f.teams = append(f.teams, teamID)
	return nil
}

// The tombstone carries the team, and the team is what scopes the delete. It
// cannot be looked up: by the time a sweep runs, the session and the team row
// that would have answered are both gone.
func TestTheObjectEffectDeletesUnderTheTeamOnTheTombstone(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	rows := &fakeObjectRows{
		rows:       []db.DeletedObject{{ObjectKey: "teams/a/objects/one", TeamID: team}},
		references: map[string]int64{},
	}
	store := &fakeObjectStore{}

	out, err := Objects(rows, store).Do(t.Context(), Item{Ref: "teams/a/objects/one", TeamID: team})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if out != Applied {
		t.Errorf("outcome = %v, want Applied", out)
	}
	if len(store.teams) != 1 || store.teams[0] != team {
		t.Errorf("deleted under %v, want [%s]", store.teams, team)
	}
}

// An object another row still names is not deleted, and the tombstone is still
// finished. Deleting the bytes would serve a live attachment its own 404.
func TestAnObjectAnotherRowStillNeedsIsSuperseded(t *testing.T) {
	t.Parallel()

	rows := &fakeObjectRows{references: map[string]int64{"shared": 1}}
	store := &fakeObjectStore{}

	out, err := Objects(rows, store).Do(t.Context(), Item{Ref: "shared"})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if out != Superseded {
		t.Errorf("outcome = %v, want Superseded", out)
	}
	if len(store.deleted) != 0 {
		t.Errorf("deleted %v while a row still referenced it", store.deleted)
	}
}

// The count authorises the delete, so a count that failed must not be read as
// a count of zero.
func TestACountFailureLeavesTheObjectAlone(t *testing.T) {
	t.Parallel()

	rows := &fakeObjectRows{references: map[string]int64{}, countErr: errors.New("reset")}
	store := &fakeObjectStore{}

	if _, err := Objects(rows, store).Do(t.Context(), Item{Ref: "one"}); err == nil {
		t.Fatal("Do reported success without knowing whether anything referenced it")
	}
	if len(store.deleted) != 0 {
		t.Errorf("deleted %v", store.deleted)
	}
}

func TestTheObjectEffectPassesTheClaimThrough(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	rows := &fakeObjectRows{
		rows: []db.DeletedObject{
			{ObjectKey: "one", TeamID: team, Attempts: 4},
			{ObjectKey: "two", TeamID: team},
		},
		references: map[string]int64{},
	}

	items, err := Objects(rows, &fakeObjectStore{}).Claim(t.Context(), time.Now(), 10)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("claimed %d", len(items))
	}
	if items[0].Ref != "one" || items[0].TeamID != team || items[0].Attempts != 4 {
		t.Errorf("item = %+v", items[0])
	}
}

type fakeSecretRows struct {
	rows []db.DeletedSecret

	forgot []string
}

func (f *fakeSecretRows) ClaimDeletedSecrets(
	_ context.Context, arg db.ClaimDeletedSecretsParams,
) ([]db.DeletedSecret, error) {
	if int32(len(f.rows)) > arg.BatchSize {
		return f.rows[:arg.BatchSize], nil
	}
	return f.rows, nil
}

func (f *fakeSecretRows) ForgetDeletedSecret(_ context.Context, path string) error {
	f.forgot = append(f.forgot, path)
	return nil
}

func (f *fakeSecretRows) DeletedSecretBacklog(context.Context) ([]db.DeletedSecretBacklogRow, error) {
	if len(f.rows) == 0 {
		return nil, nil
	}
	return []db.DeletedSecretBacklogRow{{
		Outstanding: int64(len(f.rows)), Oldest: f.rows[0].DeletedAt,
	}}, nil
}

type fakeSecretStore struct {
	err error

	deleted []string
}

func (f *fakeSecretStore) Delete(_ context.Context, path string) error {
	if f.err != nil {
		return f.err
	}
	f.deleted = append(f.deleted, path)
	return nil
}

// A secret path belongs to one channel or one team, so there is nothing to
// count and no Superseded outcome to reach.
func TestTheSecretEffectDestroysThePath(t *testing.T) {
	t.Parallel()

	rows := &fakeSecretRows{}
	store := &fakeSecretStore{}

	path := "teams/" + uuid.NewString() + "/attachments"
	out, err := Secrets(rows, store).Do(t.Context(), Item{Ref: path})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if out != Applied {
		t.Errorf("outcome = %v, want Applied", out)
	}
	if len(store.deleted) != 1 || store.deleted[0] != path {
		t.Errorf("deleted %v, want [%s]", store.deleted, path)
	}
}

func TestASecretThatWillNotDeleteIsAnError(t *testing.T) {
	t.Parallel()

	store := &fakeSecretStore{err: errors.New("openbao is sealed")}

	if _, err := Secrets(&fakeSecretRows{}, store).Do(t.Context(), Item{Ref: "p"}); err == nil {
		t.Fatal("Do reported success after the secret store refused")
	}
}

func TestTheSecretEffectPassesTheClaimThrough(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	rows := &fakeSecretRows{rows: []db.DeletedSecret{
		{Path: "teams/x/attachments", TeamID: team, Attempts: 2},
	}}

	items, err := Secrets(rows, &fakeSecretStore{}).Claim(t.Context(), time.Now(), 10)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("claimed %d", len(items))
	}
	if items[0].Ref != "teams/x/attachments" || items[0].TeamID != team || items[0].Attempts != 2 {
		t.Errorf("item = %+v", items[0])
	}
}

// Both effects report their own name, because one command runs both and a log
// line that does not say which is a log line nobody can act on.
func TestEachEffectNamesItself(t *testing.T) {
	t.Parallel()

	for want, e := range map[string]Effect{
		"objects": Objects(&fakeObjectRows{}, &fakeObjectStore{}),
		"secrets": Secrets(&fakeSecretRows{}, &fakeSecretStore{}),
	} {
		if got := e.Name(); got != want {
			t.Errorf("Name() = %q, want %q", got, want)
		}
	}
}
