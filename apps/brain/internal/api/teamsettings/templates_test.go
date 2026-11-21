package teamsettings

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/db"
)

type wanted struct {
	team, key uuid.UUID
	retry     time.Duration
}

type fakeTemplates struct {
	row   *db.E2BTemplate
	err   error
	wants []wanted
	reads int
}

func (f *fakeTemplates) Want(_ context.Context, team, key uuid.UUID, retry time.Duration) (db.E2BTemplate, error) {
	f.wants = append(f.wants, wanted{team, key, retry})
	if f.err != nil {
		return db.E2BTemplate{}, f.err
	}
	if f.row == nil {
		f.row = &db.E2BTemplate{Name: "openarity-sandbox-abc", Status: "building", UpdatedAt: time.Now()}
	}
	return *f.row, nil
}

func (f *fakeTemplates) Current(context.Context, uuid.UUID, uuid.UUID) (db.E2BTemplate, bool, error) {
	f.reads++
	if f.err != nil {
		return db.E2BTemplate{}, false, f.err
	}
	if f.row == nil {
		return db.E2BTemplate{}, false, nil
	}
	return *f.row, true, nil
}

func TestSavingE2BAsksForTheTeamsTemplateAtOnce(t *testing.T) {
	e := newEnv()
	tpl := &fakeTemplates{}
	e.templates = tpl
	got := decode(t, e.save(t, withSandbox(`{"provider":"e2b","api_key":"e2b-key"}`)))

	key := e.store.upserts[0].SandboxSecretID
	if len(tpl.wants) != 1 || tpl.wants[0].team != e.team || tpl.wants[0].key != *key || tpl.wants[0].retry != 0 {
		t.Fatalf("asked %+v, want one ask for the saved key, retrying a failed build", tpl.wants)
	}
	b := got.Sandbox.TemplateBuild
	if b == nil || b.Status != "building" || b.Name != "openarity-sandbox-abc" {
		t.Fatalf("template_build = %+v", b)
	}
}

func TestReadingSettingsShowsTheBuildAndAsksForNone(t *testing.T) {
	e := newEnv()
	id := uuid.New()
	e.storedSandbox("e2b", &id, nil)
	reason := "the image could not be pulled"
	tpl := &fakeTemplates{row: &db.E2BTemplate{Name: "n", Status: "failed", Error: &reason}}
	e.templates = tpl

	got := decode(t, e.get(t))
	if len(tpl.wants) != 0 || tpl.reads != 1 {
		t.Fatalf("%d asks, %d reads: a read started a build", len(tpl.wants), tpl.reads)
	}
	if b := got.Sandbox.TemplateBuild; b == nil || b.Status != "failed" || *b.Error != reason {
		t.Fatalf("template_build = %+v", b)
	}

	tpl.row = nil
	if got := decode(t, e.get(t)); got.Sandbox.TemplateBuild != nil {
		t.Fatalf("no build yet, but template_build = %+v", got.Sandbox.TemplateBuild)
	}
}

func TestOnlyAnE2BTeamWithNoTemplateOfItsOwnHasABuild(t *testing.T) {
	for name, body := range map[string]string{
		"its own template": `{"provider":"e2b","template":"ours","api_key":"k"}`,
		"daytona":          `{"provider":"daytona","url":"https://daytona.example","api_key":"k"}`,
		"no sandbox":       `{"provider":"none"}`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv()
			tpl := &fakeTemplates{}
			e.templates = tpl
			got := decode(t, e.save(t, withSandbox(body)))
			if len(tpl.wants) != 0 || tpl.reads != 0 || got.Sandbox.TemplateBuild != nil {
				t.Fatalf("asked %+v, read %d, build %+v", tpl.wants, tpl.reads, got.Sandbox.TemplateBuild)
			}
		})
	}
}

// The settings are saved whether or not E2B could be asked; the team's first
// room asks again.
func TestASaveStandsWhenTheBuildCannotBeAskedFor(t *testing.T) {
	e := newEnv()
	e.templates = &fakeTemplates{err: errAny}
	rec := e.save(t, withSandbox(`{"provider":"e2b","api_key":"e2b-key"}`))
	if rec.Code != http.StatusOK || len(e.store.upserts) != 1 || len(e.store.recorded) != 0 {
		t.Fatalf("%d, %d upserts, %d keys forgotten: %s", rec.Code, len(e.store.upserts), len(e.store.recorded), rec.Body)
	}
	if got := decode(t, rec); got.Sandbox.TemplateBuild != nil {
		t.Fatalf("template_build = %+v", got.Sandbox.TemplateBuild)
	}
	if strings.Contains(rec.Body.String(), "e2b-key") {
		t.Fatal("the key is in the response")
	}
}

func TestABrainThatCannotKeepRoomsShowsNoBuild(t *testing.T) {
	e := newEnv()
	got := decode(t, e.save(t, withSandbox(`{"provider":"e2b","api_key":"k"}`)))
	if got.Sandbox.TemplateBuild != nil {
		t.Fatalf("template_build = %+v", got.Sandbox.TemplateBuild)
	}
}
