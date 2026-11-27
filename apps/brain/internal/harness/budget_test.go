package harness

import "testing"

// Fresh input and output count in full; a cached input token as a tenth,
// rounded down, as the store's run_spent counts it.
func TestSpentCountsACachedTokenAsATenth(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ in, cached, out, want int64 }{
		{1_932_450, 1_695_603, 167_122, 573_529},
		{100, 0, 20, 120},
		{100, 100, 0, 10},
		{100, 9, 0, 91},
		{0, 0, 0, 0},
	} {
		if got := Spent(c.in, c.cached, c.out); got != c.want {
			t.Errorf("Spent(%d, %d, %d) = %d, want %d", c.in, c.cached, c.out, got, c.want)
		}
	}
}

func TestARunWithNoBudgetOfItsOwnHasTheDefault(t *testing.T) {
	t.Parallel()
	if got := BudgetOf(nil); got != 1_000_000 {
		t.Errorf("BudgetOf(nil) = %d", got)
	}
	two := int64(2_000_000)
	if got := BudgetOf(&two); got != two {
		t.Errorf("BudgetOf(2M) = %d", got)
	}
}
