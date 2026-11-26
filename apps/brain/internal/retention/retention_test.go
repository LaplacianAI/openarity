package retention

import (
	"context"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/jackc/pgx/v5"
	"github.com/robfig/cron/v3"

	"github.com/LaplacianAI/openarity/apps/brain/internal/store/storetest"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// The schedule is stored state, read by DBOS's parser, seconds first.
func TestTheScheduleIsDailyAndNamedForThePackage(t *testing.T) {
	t.Parallel()
	j := NewJob(quiet())
	if j.Name() != "retention" {
		t.Errorf("Name = %q", j.Name())
	}
	specs := j.schedules()
	if len(specs) != 1 || specs[0].ScheduleName != schedule || specs[0].WorkflowName != workflow ||
		specs[0].AutomaticBackfill || !strings.HasPrefix(workflow, "openarity.") {
		t.Fatalf("schedules = %+v", specs)
	}
	p := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	s, err := p.Parse(daily)
	if err != nil {
		t.Fatalf("%q does not parse: %v", daily, err)
	}
	first := s.Next(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	if first.Hour() != 3 || first.Minute() != 40 || s.Next(first).Sub(first) != 24*time.Hour {
		t.Errorf("%q runs at %v", daily, first)
	}
}

func TestRegisteringWithoutTheRuntimeRefuses(t *testing.T) {
	t.Parallel()
	if _, err := NewJob(quiet()).Register(nil); err == nil {
		t.Error("Register(nil) succeeded")
	}
}

// Old finished batches go; newer ones, and a workflow it was not told of,
// stay.
func TestItDeletesOnlyOldFinishedWorkItWasToldOf(t *testing.T) {
	dsn := storetest.Empty(t)
	schema := fmt.Sprintf("dbos_test_%08x", crc32.ChecksumIEEE([]byte(t.Name())))
	d, err := dbos.NewContext(context.WithoutCancel(t.Context()), dbos.Config{
		AppName: "openarity-brain-test", DatabaseURL: dsn, DatabaseSchema: schema, ExecutorID: "w1", Logger: quiet(),
	})
	if err != nil {
		t.Fatalf("dbos.NewContext: %v", err)
	}
	work := func(_ dbos.Context, in string) (string, error) { return in, nil }
	dbos.RegisterWorkflow(d, work, dbos.WithWorkflowName("openarity.test.batch"))
	other := func(_ dbos.Context, in string) (string, error) { return in, nil }
	dbos.RegisterWorkflow(d, other, dbos.WithWorkflowName("openarity.test.run"))
	j := NewJob(quiet(), "openarity.test.batch")
	if _, err := j.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := d.Launch(); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { _ = dbos.Shutdown(d, 5*time.Second) }) })

	for id, fn := range map[string]func(dbos.Context, string) (string, error){"old": work, "new": work, "run": other} {
		h, err := dbos.RunWorkflow(d, fn, id, dbos.WithWorkflowID(id))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.GetResult(); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	long := time.Now().Add(-8 * 24 * time.Hour).UnixMilli()
	if _, err := conn.Exec(t.Context(), `UPDATE `+schema+`.workflow_status SET completed_at = $1
		WHERE workflow_uuid IN ('old', 'run')`, long); err != nil {
		t.Fatal(err)
	}

	n, err := j.once(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("once = %d, %v; want the one old batch", n, err)
	}
	for id, want := range map[string]int{"old": 0, "new": 1, "run": 1} {
		var got int
		if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM `+schema+`.workflow_status
			WHERE workflow_uuid = $1`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s: %d recorded, want %d", id, got, want)
		}
	}
}

func TestMain(m *testing.M) { os.Exit(storetest.Main(m)) }
