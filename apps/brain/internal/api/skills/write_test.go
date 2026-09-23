package skills

import (
	"context"
	"crypto/sha256"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/LaplacianAI/openarity/apps/brain/internal/objects"
	"github.com/LaplacianAI/openarity/apps/brain/internal/skill"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

// txStore stands in for Postgres. It owes every refusal the real one makes
// that a write depends on: a transaction that fails leaves nothing behind, a
// name is unique in its team, and a skill that is not there has no rows.
type txStore struct {
	mu       sync.Mutex
	log      []string
	skills   map[uuid.UUID]db.Skill
	files    map[uuid.UUID][]db.InsertSkillFilesParams
	reserved []db.ReserveObjectsParams

	reserveErr error
	failAt     string
}

func newTxStore(existing ...db.Skill) *txStore {
	s := &txStore{skills: map[uuid.UUID]db.Skill{}, files: map[uuid.UUID][]db.InsertSkillFilesParams{}}
	for _, row := range existing {
		s.skills[row.ID] = row
	}
	return s
}

func (s *txStore) record(step string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, step)
}

func (s *txStore) steps() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.log)
}

func (s *txStore) ReserveObjects(ctx context.Context, arg db.ReserveObjectsParams) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.record("ReserveObjects")
	if s.reserveErr != nil {
		return s.reserveErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reserved = append(s.reserved, arg)
	return nil
}

// InTx runs fn against copies and keeps them only if fn succeeds.
func (s *txStore) InTx(ctx context.Context, fn func(Queries) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.record("InTx")

	s.mu.Lock()
	tx := &txQueries{s: s, skills: maps.Clone(s.skills), files: maps.Clone(s.files)}
	s.mu.Unlock()

	if err := fn(tx); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.skills, s.files = tx.skills, tx.files
	return nil
}

type txQueries struct {
	s      *txStore
	skills map[uuid.UUID]db.Skill
	files  map[uuid.UUID][]db.InsertSkillFilesParams
}

func (q *txQueries) step(name string) error {
	q.s.record(name)
	if q.s.failAt == name {
		return errors.New(name + " failed")
	}
	return nil
}

func (q *txQueries) nameTaken(teamID uuid.UUID, name string, except uuid.UUID) bool {
	for id, row := range q.skills {
		if id != except && row.TeamID == teamID && row.Name == name {
			return true
		}
	}
	return false
}

func (q *txQueries) CreateSkill(_ context.Context, arg db.CreateSkillParams) (db.Skill, error) {
	if err := q.step("CreateSkill"); err != nil {
		return db.Skill{}, err
	}
	if q.nameTaken(arg.TeamID, arg.Name, uuid.Nil) {
		return db.Skill{}, &pgconn.PgError{Code: codeUniqueViolation}
	}
	row := db.Skill{
		ID: uuid.New(), TeamID: arg.TeamID, Name: arg.Name, Description: arg.Description,
		License: arg.License, Compatibility: arg.Compatibility, Metadata: arg.Metadata,
		AllowedTools: arg.AllowedTools, Body: arg.Body,
		Source: arg.Source, SourceRef: arg.SourceRef, SourceSha: arg.SourceSha,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	q.skills[row.ID] = row
	return row, nil
}

func (q *txQueries) UpdateSkill(_ context.Context, arg db.UpdateSkillParams) (db.Skill, error) {
	if err := q.step("UpdateSkill"); err != nil {
		return db.Skill{}, err
	}
	row, ok := q.skills[arg.ID]
	if !ok {
		return db.Skill{}, pgx.ErrNoRows
	}
	if q.nameTaken(row.TeamID, arg.Name, arg.ID) {
		return db.Skill{}, &pgconn.PgError{Code: codeUniqueViolation}
	}
	row.Name, row.Description, row.Body = arg.Name, arg.Description, arg.Body
	row.License, row.Compatibility, row.AllowedTools = arg.License, arg.Compatibility, arg.AllowedTools
	row.Metadata = arg.Metadata
	row.Source, row.SourceRef, row.SourceSha = arg.Source, arg.SourceRef, arg.SourceSha
	q.skills[arg.ID] = row
	return row, nil
}

func (q *txQueries) ClearSkillFiles(_ context.Context, skillID uuid.UUID) error {
	if err := q.step("ClearSkillFiles"); err != nil {
		return err
	}
	delete(q.files, skillID)
	return nil
}

func (q *txQueries) InsertSkillFiles(_ context.Context, rows []db.InsertSkillFilesParams) (int64, error) {
	if err := q.step("InsertSkillFiles"); err != nil {
		return 0, err
	}
	for _, r := range rows {
		if _, ok := q.skills[r.SkillID]; !ok {
			return 0, errors.New("foreign key: no such skill")
		}
		q.files[r.SkillID] = append(q.files[r.SkillID], r)
	}
	return int64(len(rows)), nil
}

// bucket stands in for the encrypted object store: it refuses a key outside
// the team, as objects.Encrypted does, and can fail the nth put.
type bucket struct {
	mu      sync.Mutex
	store   *txStore
	objects map[string][]byte
	failOn  int
	getErr  error
}

func newBucket(s *txStore) *bucket {
	return &bucket{store: s, objects: map[string][]byte{}}
}

func (b *bucket) Put(ctx context.Context, teamID uuid.UUID, key string, body []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.store.record("Put")
	if !objects.InTeam(key, teamID) {
		return objects.ErrWrongTeam
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failOn == len(b.objects)+1 {
		return errors.New("bucket unavailable")
	}
	b.objects[key] = body
	return nil
}

func (b *bucket) Get(ctx context.Context, teamID uuid.UUID, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !objects.InTeam(key, teamID) {
		return nil, objects.ErrWrongTeam
	}
	if b.getErr != nil {
		return nil, b.getErr
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	body, ok := b.objects[key]
	if !ok {
		return nil, objects.ErrNotFound
	}
	return body, nil
}

func directory(t *testing.T, entries ...skill.Entry) skill.Directory {
	t.Helper()

	dir, err := skill.Assemble(entries)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	return dir
}

func pdfSkill(t *testing.T) skill.Directory {
	return directory(t,
		skill.Entry{Path: "SKILL.md", Data: []byte(
			"---\nname: pdf\ndescription: Fill PDF forms.\nlicense: MIT\nmetadata:\n  author: ops\n---\n# PDF\n")},
		skill.Entry{Path: "scripts/fill.py", Data: []byte("print('fill')\n")},
		skill.Entry{Path: "references/FORMS.md", Data: []byte("# Forms\n")},
	)
}

func text(s string) *string { return &s }

// Every file is stored under a key of its own, in the team, and the row that
// names it names the bytes that were put there — matched by key, path, size
// and hash, which is everything the read path trusts.
func TestCreateStoresEveryFileUnderItsOwnReservedKey(t *testing.T) {
	t.Parallel()

	s := newTxStore()
	b := newBucket(s)
	team := uuid.New()
	dir := pdfSkill(t)

	row, err := writer{store: s, objects: b}.create(t.Context(), team, dir, uploaded)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	files := s.files[row.ID]
	if len(files) != len(dir.Files) {
		t.Fatalf("%d file rows, want %d", len(files), len(dir.Files))
	}
	if len(s.reserved) != 1 {
		t.Fatalf("%d reservations, want one for the whole upload", len(s.reserved))
	}

	seen := map[string]bool{}
	for i, f := range files {
		want := dir.Files[i]
		if f.Path != want.Path || f.Size != int64(len(want.Data)) || f.MediaType != want.MediaType {
			t.Errorf("row %d = %+v, want the file %s", i, f, want.Path)
		}
		if sum := sha256.Sum256(want.Data); string(f.Sha256) != string(sum[:]) {
			t.Errorf("%s: row hash is not of the file's bytes", f.Path)
		}
		if string(b.objects[f.ObjectKey]) != string(want.Data) {
			t.Errorf("%s: the object under its key holds %q", f.Path, b.objects[f.ObjectKey])
		}
		if !slices.Contains(s.reserved[0].ObjectKeys, f.ObjectKey) {
			t.Errorf("%s: key %s was never reserved, so an abandoned upload would leave it forever", f.Path, f.ObjectKey)
		}
		if !strings.HasPrefix(f.ObjectKey, objects.TeamPrefix(team)+"objects/") {
			t.Errorf("%s: key %s is not a team object key", f.Path, f.ObjectKey)
		}
		if seen[f.ObjectKey] {
			t.Errorf("two files share the key %s", f.ObjectKey)
		}
		seen[f.ObjectKey] = true
		if f.SkillID != row.ID || f.TeamID != team {
			t.Errorf("%s: row belongs to skill %s team %s", f.Path, f.SkillID, f.TeamID)
		}
	}
}

// Reserve, then put, then commit. Put first and a crash before the
// reservation leaves an object no tombstone names — nothing would ever sweep
// it. Commit before the puts and a committed row names bytes not yet there.
func TestAWriteReservesThenPutsThenCommits(t *testing.T) {
	t.Parallel()

	s := newTxStore()
	if _, err := (writer{store: s, objects: newBucket(s)}).create(t.Context(), uuid.New(), pdfSkill(t), uploaded); err != nil {
		t.Fatalf("create: %v", err)
	}

	want := []string{"ReserveObjects", "Put", "Put", "InTx", "CreateSkill", "InsertSkillFiles"}
	if got := s.steps(); !slices.Equal(got, want) {
		t.Errorf("steps = %v, want %v", got, want)
	}
}

// The hold is what keeps the reaper off a key between the put and the
// commit. Reserved as claimable now, a sweep in that window finds no row and
// deletes the object the skill is about to name.
func TestAReservationIsHeldForTheWholeHold(t *testing.T) {
	t.Parallel()

	s := newTxStore()
	before := time.Now()
	if _, err := (writer{store: s, objects: newBucket(s)}).create(t.Context(), uuid.New(), pdfSkill(t), uploaded); err != nil {
		t.Fatalf("create: %v", err)
	}
	after := time.Now()

	got := s.reserved[0].ClaimableAfter
	if got.Before(before.Add(reservationHold)) || got.After(after.Add(reservationHold)) {
		t.Errorf("claimable after %v, want %v from now", got.Sub(before), reservationHold)
	}
}

// The request's own deadline is thirty seconds, but an import downloads
// before it writes. An hour is the margin; a hold shorter than the slowest
// write is the race the reservation exists to close.
func TestTheHoldOutlastsAnyWrite(t *testing.T) {
	t.Parallel()

	if reservationHold < 10*time.Minute {
		t.Errorf("reservationHold = %v; a write slower than that loses its objects to the reaper", reservationHold)
	}
}

// A failed put stops the write before anything reaches Postgres. The keys
// stay reserved: they are the reaper's record of what to sweep, and removing
// them here would be a second, weaker reaper.
func TestAFailedPutWritesNoRowsAndLeavesTheReservation(t *testing.T) {
	t.Parallel()

	s := newTxStore()
	b := newBucket(s)
	b.failOn = 2

	_, err := writer{store: s, objects: b}.create(t.Context(), uuid.New(), pdfSkill(t), uploaded)
	if err == nil || !strings.Contains(err.Error(), "scripts/fill.py") {
		t.Fatalf("err = %v, want the failing file named", err)
	}
	if got := s.steps(); slices.Contains(got, "InTx") {
		t.Errorf("steps = %v; a failed put must not reach the transaction", got)
	}
	if len(s.skills) != 0 {
		t.Errorf("a skill was written: %v", s.skills)
	}
	if len(s.reserved) != 1 || len(s.reserved[0].ObjectKeys) != 2 {
		t.Errorf("reservations = %+v, want both keys left for the reaper", s.reserved)
	}
}

// Without a reservation nothing may be put: the object would have no record.
func TestAFailedReservationPutsNothing(t *testing.T) {
	t.Parallel()

	s := newTxStore()
	s.reserveErr = errors.New("connection refused")
	b := newBucket(s)

	if _, err := (writer{store: s, objects: b}).create(t.Context(), uuid.New(), pdfSkill(t), uploaded); err == nil {
		t.Fatal("accepted")
	}
	if len(b.objects) != 0 || slices.Contains(s.steps(), "InTx") {
		t.Errorf("steps = %v, objects = %d; nothing should follow a failed reservation", s.steps(), len(b.objects))
	}
}

// The handler turns this into a 409, so the code has to survive the writer.
func TestATakenNameComesBackAsTheUniqueViolation(t *testing.T) {
	t.Parallel()

	team := uuid.New()
	s := newTxStore(db.Skill{ID: uuid.New(), TeamID: team, Name: "pdf"})

	_, err := writer{store: s, objects: newBucket(s)}.create(t.Context(), team, pdfSkill(t), uploaded)
	if !hasCode(err, codeUniqueViolation) {
		t.Errorf("err = %v, want the unique violation", err)
	}
	if len(s.skills) != 1 {
		t.Errorf("%d skills, want only the one that held the name", len(s.skills))
	}
}

// A skill with no rows for its files cannot be loaded past its body; one that
// committed without them would look whole and be missing everything it links.
func TestASkillIsNotCommittedWithoutItsFiles(t *testing.T) {
	t.Parallel()

	s := newTxStore()
	s.failAt = "InsertSkillFiles"

	if _, err := (writer{store: s, objects: newBucket(s)}).create(t.Context(), uuid.New(), pdfSkill(t), uploaded); err == nil {
		t.Fatal("accepted")
	}
	if len(s.skills) != 0 {
		t.Errorf("the skill committed without its files: %v", s.skills)
	}
}

// Everything the manifest says reaches the row, and the origin says who
// wrote it. An upload records no ref and no commit; the database refuses an
// upload that does.
func TestTheManifestAndOriginReachTheRow(t *testing.T) {
	t.Parallel()

	s := newTxStore()
	w := writer{store: s, objects: newBucket(s)}

	up, err := w.create(t.Context(), uuid.New(), pdfSkill(t), uploaded)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if up.Name != "pdf" || up.Description != "Fill PDF forms." || up.Body != "# PDF\n" ||
		up.License == nil || *up.License != "MIT" || up.Compatibility != nil || up.AllowedTools != nil {
		t.Errorf("row = %+v", up)
	}
	if string(up.Metadata) != `{"author":"ops"}` {
		t.Errorf("metadata = %s", up.Metadata)
	}
	if up.Source != "upload" || up.SourceRef != nil || up.SourceSha != nil {
		t.Errorf("upload origin = %s %v %v", up.Source, up.SourceRef, up.SourceSha)
	}

	gh := Origin{Source: "github", Ref: text("github:anthropics/skills/pdf@main"), SHA: text("0123abc")}
	imported, err := w.create(t.Context(), uuid.New(), pdfSkill(t), gh)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if imported.Source != "github" || *imported.SourceRef != *gh.Ref || *imported.SourceSha != *gh.SHA {
		t.Errorf("import origin = %s %v %v", imported.Source, imported.SourceRef, imported.SourceSha)
	}
}

// metadata is a NOT NULL jsonb. Absent, it is {}, never null.
func TestAbsentMetadataIsStoredAsAnEmptyObject(t *testing.T) {
	t.Parallel()

	s := newTxStore()
	dir := directory(t, skill.Entry{Path: "SKILL.md", Data: []byte("---\nname: a\ndescription: d\n---\n")})

	row, err := writer{store: s, objects: newBucket(s)}.create(t.Context(), uuid.New(), dir, uploaded)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if string(row.Metadata) != "{}" {
		t.Errorf("metadata = %s, want {}", row.Metadata)
	}
}

// A skill that is only its SKILL.md reserves nothing, puts nothing, and sends
// no empty COPY.
func TestASkillWithNoFilesTouchesNoObjects(t *testing.T) {
	t.Parallel()

	s := newTxStore()
	dir := directory(t, skill.Entry{Path: "SKILL.md", Data: []byte("---\nname: a\ndescription: d\n---\n")})

	if _, err := (writer{store: s, objects: newBucket(s)}).create(t.Context(), uuid.New(), dir, uploaded); err != nil {
		t.Fatalf("create: %v", err)
	}
	if want := []string{"InTx", "CreateSkill"}; !slices.Equal(s.steps(), want) {
		t.Errorf("steps = %v, want %v", s.steps(), want)
	}
}

func existingPDF(t *testing.T) (*txStore, *bucket, db.Skill) {
	t.Helper()

	s := newTxStore()
	b := newBucket(s)
	row, err := writer{store: s, objects: b}.create(t.Context(), uuid.New(), pdfSkill(t), uploaded)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s.log = nil
	return s, b, row
}

// A replace is whole: files the new upload dropped are gone, and every file
// is under a fresh key. Reusing the old keys would overwrite bytes a reader
// may be streaming, and the delete trigger would tombstone keys the new rows
// still name.
func TestReplaceSwapsTheWholeDirectoryForFreshKeys(t *testing.T) {
	t.Parallel()

	s, b, old := existingPDF(t)
	oldKeys := map[string]bool{}
	for _, f := range s.files[old.ID] {
		oldKeys[f.ObjectKey] = true
	}

	dir := directory(t,
		skill.Entry{Path: "SKILL.md", Data: []byte("---\nname: pdf\ndescription: Now also merges.\n---\n# PDF v2\n")},
		skill.Entry{Path: "scripts/merge.py", Data: []byte("print('merge')\n")},
	)
	row, err := writer{store: s, objects: b}.replace(t.Context(), old.ID, old.TeamID, dir, uploaded)
	if err != nil {
		t.Fatalf("replace: %v", err)
	}

	if row.ID != old.ID || row.Description != "Now also merges." || row.Body != "# PDF v2\n" {
		t.Errorf("row = %+v", row)
	}
	files := s.files[old.ID]
	if len(files) != 1 || files[0].Path != "scripts/merge.py" {
		t.Fatalf("files = %+v, want only the new directory", files)
	}
	if oldKeys[files[0].ObjectKey] {
		t.Errorf("the new file reused the old key %s", files[0].ObjectKey)
	}
	want := []string{"ReserveObjects", "Put", "InTx", "UpdateSkill", "ClearSkillFiles", "InsertSkillFiles"}
	if got := s.steps(); !slices.Equal(got, want) {
		t.Errorf("steps = %v, want %v", got, want)
	}
}

// An imported skill replaced by an upload is the team's own now; its old
// origin no longer describes it and must not survive the replace.
func TestReplacingAnImportWithAnUploadClearsItsOrigin(t *testing.T) {
	t.Parallel()

	s := newTxStore()
	b := newBucket(s)
	w := writer{store: s, objects: b}
	gh := Origin{Source: "github", Ref: text("github:anthropics/skills/pdf@main"), SHA: text("0123abc")}
	imported, err := w.create(t.Context(), uuid.New(), pdfSkill(t), gh)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	row, err := w.replace(t.Context(), imported.ID, imported.TeamID, pdfSkill(t), uploaded)
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if row.Source != "upload" || row.SourceRef != nil || row.SourceSha != nil {
		t.Errorf("origin after upload = %s %v %v", row.Source, row.SourceRef, row.SourceSha)
	}
}

// Replacing with only a SKILL.md still clears the files: fewer files is a
// change, and "no files" is the smallest one.
func TestReplacingWithNoFilesClearsTheOldOnes(t *testing.T) {
	t.Parallel()

	s, b, old := existingPDF(t)
	dir := directory(t, skill.Entry{Path: "SKILL.md", Data: []byte("---\nname: pdf\ndescription: d\n---\n")})

	if _, err := (writer{store: s, objects: b}).replace(t.Context(), old.ID, old.TeamID, dir, uploaded); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got := s.files[old.ID]; len(got) != 0 {
		t.Errorf("files = %+v, want none", got)
	}
}

// The handler turns this into a 404. Deleted between the read and the write,
// the skill must not be resurrected, and its old files — gone with it — must
// not be cleared a second time.
func TestReplacingASkillThatIsGoneIsNoRows(t *testing.T) {
	t.Parallel()

	s, b, old := existingPDF(t)
	delete(s.skills, old.ID)

	_, err := writer{store: s, objects: b}.replace(t.Context(), old.ID, old.TeamID, pdfSkill(t), uploaded)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("err = %v, want pgx.ErrNoRows", err)
	}
	if slices.Contains(s.steps(), "ClearSkillFiles") {
		t.Errorf("steps = %v; nothing to clear for a skill that is gone", s.steps())
	}
}

// A replace that fails half-way keeps the old directory whole: the old files
// were cleared inside the transaction, and the transaction is what failed.
func TestAFailedReplaceKeepsTheOldDirectory(t *testing.T) {
	t.Parallel()

	s, b, old := existingPDF(t)
	before := fileKeys(s.files[old.ID])
	s.failAt = "InsertSkillFiles"

	if _, err := (writer{store: s, objects: b}).replace(t.Context(), old.ID, old.TeamID, pdfSkill(t), uploaded); err == nil {
		t.Fatal("accepted")
	}
	if got := fileKeys(s.files[old.ID]); !slices.Equal(got, before) {
		t.Errorf("files = %v, want the old %v", got, before)
	}
	if s.skills[old.ID].Description != old.Description {
		t.Errorf("the row changed: %+v", s.skills[old.ID])
	}
}

// A request the caller abandoned stops writing.
func TestACancelledWriteStops(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s := newTxStore()
	b := newBucket(s)

	if _, err := (writer{store: s, objects: b}).create(ctx, uuid.New(), pdfSkill(t), uploaded); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if len(b.objects) != 0 || len(s.skills) != 0 {
		t.Errorf("wrote %d objects and %d skills after cancellation", len(b.objects), len(s.skills))
	}
}

func fileKeys(rows []db.InsertSkillFilesParams) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Path + "=" + r.ObjectKey
	}
	return out
}

// However a replace fails — before Postgres or inside the transaction — the
// skill a reader loads afterwards is the old one, with its old files.
func TestEveryFailedReplaceLeavesTheOldSkillWhole(t *testing.T) {
	t.Parallel()

	for name, breakIt := range map[string]func(*txStore, *bucket){
		"a put fails":              func(_ *txStore, b *bucket) { b.failOn = len(b.objects) + 1 },
		"the reservation fails":    func(s *txStore, _ *bucket) { s.reserveErr = errors.New("connection refused") },
		"clearing the files fails": func(s *txStore, _ *bucket) { s.failAt = "ClearSkillFiles" },
		"the update fails":         func(s *txStore, _ *bucket) { s.failAt = "UpdateSkill" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, b, old := existingPDF(t)
			before := fileKeys(s.files[old.ID])
			breakIt(s, b)

			if _, err := (writer{store: s, objects: b}).replace(t.Context(), old.ID, old.TeamID, pdfSkill(t), uploaded); err == nil {
				t.Fatal("accepted")
			}
			if got := fileKeys(s.files[old.ID]); !slices.Equal(got, before) {
				t.Errorf("files = %v, want the old %v", got, before)
			}
			if s.skills[old.ID].Description != old.Description {
				t.Errorf("the row changed: %+v", s.skills[old.ID])
			}
		})
	}
}
