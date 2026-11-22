package keeper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/LaplacianAI/openarity/apps/brain/internal/sandbox"
	"github.com/LaplacianAI/openarity/apps/brain/internal/secrets"
	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

const (
	// RetryFailedAfter is how long a failed build is left before a room's
	// start asks for another. A save in Settings asks at once.
	RetryFailedAfter = 10 * time.Minute
	// BuildTimeout is how long a build may run before it is called failed.
	BuildTimeout = 30 * time.Minute
	// sweepMax is how many running builds one sweep reads back.
	sweepMax = 50

	// The states of a template's row, as the table names them.
	TemplateBuilding = "building"
	TemplateReady    = "ready"
	TemplateFailed   = "failed"
)

// TemplateStore is the part of the database the template builder uses.
type TemplateStore interface {
	WithE2BTemplateLock(ctx context.Context, team, key uuid.UUID, name string, fn func(context.Context) error) error
	GetE2BTemplate(ctx context.Context, arg db.GetE2BTemplateParams) (db.E2BTemplate, error)
	GetReadyE2BTemplate(ctx context.Context, arg db.GetReadyE2BTemplateParams) (db.E2BTemplate, error)
	SaveE2BTemplate(ctx context.Context, arg db.SaveE2BTemplateParams) (db.E2BTemplate, error)
	ListBuildingE2BTemplates(ctx context.Context, maxRows int32) ([]db.E2BTemplate, error)
	FinishE2BTemplate(ctx context.Context, arg db.FinishE2BTemplateParams) (int64, error)
	TouchE2BTemplate(ctx context.Context, id uuid.UUID) error
}

// TemplateClient is E2B, as one team's key opens it.
type TemplateClient interface {
	BuildTemplate(ctx context.Context, name, image string, size *sandbox.Size) (sandbox.Build, error)
	BuildStatus(ctx context.Context, b sandbox.Build) (sandbox.BuildState, string, error)
}

// Templates builds the room image into each E2B team's account, as a template
// named for the image, so an E2B team's rooms start from the release's image
// as every other provider's do, and the team does nothing for it.
type Templates struct {
	store   TemplateStore
	secrets secrets.Store
	connect func(key string) TemplateClient
	images  Images
	logger  *slog.Logger
	now     func() time.Time

	// ready holds the teams' keys whose template of this brain's name is
	// built, so a room's start asks the database only until it is.
	ready sync.Map
	// warned holds the failed builds already logged as covered by an older
	// template, so a room's every start does not log it again.
	warned sync.Map
}

// NewTemplates returns the builder.
func NewTemplates(s TemplateStore, sec secrets.Store, connect func(key string) TemplateClient, images Images, logger *slog.Logger) *Templates {
	if logger == nil {
		panic("keeper: a nil logger")
	}
	return &Templates{store: s, secrets: sec, connect: connect, images: images, logger: logger, now: time.Now}
}

// At sets the clock the builder reads. It is for tests, which cannot wait
// ten minutes for a retry.
func (t *Templates) At(now func() time.Time) *Templates {
	t.now = now
	return t
}

// E2BName is the template the brain's room image is built as at size, nil
// for the default: named for the release's commit, so an upgrade builds a new
// one, which rooms move to once it is built, and for the size, so a team's
// workspaces get a template of their own beside its command servers'. A brain
// that does not know its commit builds -latest once, and an override is named
// for its reference: neither notices when a mutable tag moves, so a new image
// under the same tag needs the row deleted to rebuild.
func (i Images) E2BName(size *sandbox.Size) string {
	name := E2BTemplate + "-" + i.e2bRelease()
	if k := sizeKey(size); k != "" {
		name += "-" + k
	}
	return name
}

func (i Images) e2bRelease() string {
	if i.Override == "" && i.Commit != "" {
		c := i.Commit
		if len(c) > 12 {
			c = c[:12]
		}
		return c
	}
	if i.Override == "" {
		return "latest"
	}
	sum := sha256.Sum256([]byte(i.Override))
	return hex.EncodeToString(sum[:6])
}

// sizeKey is a size as a template's name and row carry it: the empty string
// for the default, '<cpus>c-<memory>m-<disk>d' otherwise.
func sizeKey(size *sandbox.Size) string {
	if size == nil {
		return ""
	}
	return fmt.Sprintf("%dc-%dm-%dd", size.CPUs, size.MemoryMB, size.DiskMB)
}

// Want is the team's build of the template under key at size (nil for the
// default): the one there is, or one asked for now when there is none or the
// last failed retry or more ago; a retry of 0 asks again for any failed build.
// A build E2B refused to start is saved as failed and returned without error.
func (t *Templates) Want(ctx context.Context, team, key uuid.UUID, size *sandbox.Size, retry time.Duration) (db.E2BTemplate, error) {
	name := t.images.E2BName(size)
	var row db.E2BTemplate
	err := t.store.WithE2BTemplateLock(ctx, team, key, name, func(ctx context.Context) error {
		got, err := t.store.GetE2BTemplate(ctx, db.GetE2BTemplateParams{TeamID: team, SandboxSecretID: key, Name: name})
		switch {
		case err == nil && (got.Status != TemplateFailed || (retry > 0 && t.now().Sub(got.UpdatedAt) < retry)):
			row = got
			return nil
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("reading the team's room template: %w", err)
		}
		row, err = t.start(ctx, team, key, name, size)
		return err
	})
	return row, err
}

// Current is the team's build of the template under key at size, if one was
// ever asked for; it asks for none.
func (t *Templates) Current(ctx context.Context, team, key uuid.UUID, size *sandbox.Size) (db.E2BTemplate, bool, error) {
	row, err := t.store.GetE2BTemplate(ctx, db.GetE2BTemplateParams{TeamID: team, SandboxSecretID: key, Name: t.images.E2BName(size)})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.E2BTemplate{}, false, nil
	}
	if err != nil {
		return db.E2BTemplate{}, false, fmt.Errorf("reading the team's room template: %w", err)
	}
	return row, true, nil
}

// Image is the template a room of the team's starts from under key at size
// (nil for the default), once it is built. While it is building, or after it
// failed, rooms start from the newest template of that size already built
// under the key, so an upgrade does not stop a team's rooms; a team with
// none built yet gets sandbox.ErrPreparing while it builds and
// sandbox.ErrRefused, with E2B's reason, when it failed.
func (t *Templates) Image(ctx context.Context, team, key uuid.UUID, size *sandbox.Size) (string, error) {
	name := t.images.E2BName(size)
	cached := key.String() + ":" + name
	if _, ok := t.ready.Load(cached); ok {
		return name, nil
	}
	row, err := t.Want(ctx, team, key, size, RetryFailedAfter)
	if err != nil {
		return "", err
	}
	if row.Status == TemplateReady {
		t.ready.Store(cached, struct{}{})
		return name, nil
	}
	prev, found, err := t.lastReady(ctx, team, key, size)
	if err != nil {
		return "", err
	}
	if found {
		if _, seen := t.warned.LoadOrStore(row.ID.String()+":"+row.UpdatedAt.String(), struct{}{}); !seen && row.Status == TemplateFailed {
			t.logger.WarnContext(ctx, "keeper: this release's room template failed to build; rooms start from the last one built",
				"team_id", team, "template", name, "instead", prev, "error", deref(row.Error))
		}
		return prev, nil
	}
	switch row.Status {
	case TemplateBuilding:
		return "", sandbox.ErrPreparing
	default:
		reason := "it failed"
		if row.Error != nil {
			reason = *row.Error
		}
		return "", fmt.Errorf("%w: the team's sandbox template did not build at E2B (%s). Nothing ran, "+
			"and calling again soon will not help: tell the person, since a team admin sees why in Settings, "+
			"under Sandbox, and saving it there builds it again", sandbox.ErrRefused, reason)
	}
}

// lastReady is the newest template already built under key at size, of any
// release.
func (t *Templates) lastReady(ctx context.Context, team, key uuid.UUID, size *sandbox.Size) (string, bool, error) {
	prev, err := t.store.GetReadyE2BTemplate(ctx, db.GetReadyE2BTemplateParams{TeamID: team, SandboxSecretID: key, Size: sizeKey(size)})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("reading the team's built room templates: %w", err)
	}
	return prev.Name, true, nil
}

func (t *Templates) start(ctx context.Context, team, key uuid.UUID, name string, size *sandbox.Size) (db.E2BTemplate, error) {
	client, err := t.client(ctx, team, key)
	if err != nil {
		return db.E2BTemplate{}, err
	}
	b, err := client.BuildTemplate(ctx, name, t.images.Ref(), size)
	if err != nil && ctx.Err() != nil {
		return db.E2BTemplate{}, err
	}
	arg := db.SaveE2BTemplateParams{
		TeamID: team, SandboxSecretID: key, Name: name, Size: sizeKey(size), Status: TemplateBuilding,
		TemplateID: nonEmpty(b.TemplateID), BuildID: nonEmpty(b.BuildID),
	}
	if err != nil {
		t.logger.WarnContext(ctx, "keeper: E2B did not start a template build", "team_id", team, "error", err)
		msg := err.Error()
		arg.Status, arg.Error = TemplateFailed, &msg
	}
	row, err := t.store.SaveE2BTemplate(ctx, arg)
	if err != nil {
		return db.E2BTemplate{}, fmt.Errorf("saving the team's room template: %w", err)
	}
	return row, nil
}

// Sweep reads back the builds still running and records the ones that
// finished. A build whose status cannot be read is left for the next sweep,
// unless it has run past BuildTimeout.
func (t *Templates) Sweep(ctx context.Context) error {
	rows, err := t.store.ListBuildingE2BTemplates(ctx, sweepMax)
	if err != nil {
		return fmt.Errorf("listing the room templates being built: %w", err)
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		t.check(ctx, row)
	}
	return nil
}

func (t *Templates) check(ctx context.Context, row db.E2BTemplate) {
	state, reason := t.status(ctx, row)
	if state == sandbox.BuildRunning && t.now().Sub(row.StartedAt) >= BuildTimeout {
		state, reason = sandbox.BuildFailed, "the build ran longer than "+BuildTimeout.String()
	}

	var err error
	switch state {
	case sandbox.BuildReady:
		err = t.finish(ctx, row, TemplateReady, nil)
	case sandbox.BuildFailed:
		err = t.finish(ctx, row, TemplateFailed, &reason)
	case sandbox.BuildRunning:
		err = t.store.TouchE2BTemplate(ctx, row.ID)
	default:
		err = t.store.TouchE2BTemplate(ctx, row.ID)
	}
	if err != nil {
		t.logger.WarnContext(ctx, "keeper: a room template's build was not recorded", "template", row.ID, "error", err)
	}
}

// status is where row's build is, as E2B says. Trouble asking is logged and
// read as still running; a key that is gone fails the build, since nothing
// can read it back again.
func (t *Templates) status(ctx context.Context, row db.E2BTemplate) (sandbox.BuildState, string) {
	client, err := t.client(ctx, row.TeamID, row.SandboxSecretID)
	if errors.Is(err, secrets.ErrNotFound) {
		return sandbox.BuildFailed, "the sandbox key it was built with is gone"
	}
	if err == nil {
		var state sandbox.BuildState
		var reason string
		state, reason, err = client.BuildStatus(ctx, sandbox.Build{TemplateID: deref(row.TemplateID), BuildID: deref(row.BuildID)})
		if err == nil {
			return state, reason
		}
	}
	t.logger.WarnContext(ctx, "keeper: a room template's build could not be read", "template", row.ID, "error", err)
	return sandbox.BuildRunning, ""
}

func (t *Templates) finish(ctx context.Context, row db.E2BTemplate, status string, reason *string) error {
	_, err := t.store.FinishE2BTemplate(ctx, db.FinishE2BTemplateParams{
		ID: row.ID, BuildID: row.BuildID, Status: status, Error: reason,
	})
	return err
}

func (t *Templates) client(ctx context.Context, team, key uuid.UUID) (TemplateClient, error) {
	apiKey, err := sandboxKey(ctx, t.secrets, team, key)
	if err != nil {
		return nil, err
	}
	return t.connect(apiKey), nil
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
