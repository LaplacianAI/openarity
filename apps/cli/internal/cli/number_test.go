package cli

import "testing"

func TestCountsAreGroupedByThousands(t *testing.T) {
	t.Parallel()

	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1677: "1,677", 1234567: "1,234,567", -1677: "-1,677"} {
		if got := Grouped(n); got != want {
			t.Errorf("Grouped(%d) = %q, want %q", n, got, want)
		}
	}
}
