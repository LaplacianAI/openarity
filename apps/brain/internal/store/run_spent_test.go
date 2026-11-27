package store

import (
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/harness"
)

// run_spent, which the store counts a run's budget with, and harness.Spent,
// which the API reads it back with, agree token for token.
func TestRunSpentAgreesWithHarnessSpent(t *testing.T) {
	t.Parallel()

	s := queryStore(t)
	for _, c := range []struct{ in, cached, out int64 }{
		{1_932_450, 1_695_603, 167_122},
		{100, 9, 0},
		{100, 100, 7},
		{0, 0, 0},
		{3, 1, 0},
	} {
		var got int64
		if err := s.pool.QueryRow(t.Context(), `SELECT run_spent($1, $2, $3)`, c.in, c.cached, c.out).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if want := harness.Spent(c.in, c.cached, c.out); got != want {
			t.Errorf("run_spent(%d, %d, %d) = %d, harness.Spent = %d", c.in, c.cached, c.out, got, want)
		}
	}
}
