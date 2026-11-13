package stream

import (
	"sync"
	"testing"
)

func TestMemoryNumbersFromOneAndReadsAfter(t *testing.T) {
	var m Memory
	for i := range 5 {
		if env := m.Append("r", StepStarted{Step: i}); env.Seq != int64(i)+1 || env.At.IsZero() {
			t.Fatalf("append %d: %+v", i, env)
		}
	}
	cases := []struct {
		after int64
		limit int
		want  []int64
	}{
		{Start, 0, []int64{1, 2, 3, 4, 5}},
		{0, 0, []int64{1, 2, 3, 4, 5}},
		{2, 0, []int64{3, 4, 5}},
		{2, 2, []int64{3, 4}},
		{5, 0, nil},
		{99, 0, nil},
		{-3, 1, []int64{1}},
	}
	for _, c := range cases {
		envs, err := m.Since(t.Context(), c.after, c.limit)
		if err != nil {
			t.Fatal(err)
		}
		if got := seqs(envs); !equal(got, c.want) {
			t.Errorf("Since(%d, %d) = %v, want %v", c.after, c.limit, got, c.want)
		}
	}
}

// What Since returned is the caller's: appending afterwards must not show
// through it.
func TestMemoryHandsOutCopies(t *testing.T) {
	var m Memory
	m.Append("r", StepStarted{Step: 1})
	envs, _ := m.Since(t.Context(), Start, 0)
	envs[0].RunID = "changed"
	again, _ := m.Since(t.Context(), Start, 0)
	if again[0].RunID != "r" {
		t.Fatal("a caller's change reached the log")
	}
}

func TestMemoryNumbersConcurrentAppendsOnce(t *testing.T) {
	var m Memory
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() { m.Append("r", StepStarted{}) })
	}
	wg.Wait()
	envs, _ := m.Since(t.Context(), Start, 0)
	for i, env := range envs {
		if env.Seq != int64(i)+1 {
			t.Fatalf("event %d has seq %d", i, env.Seq)
		}
	}
	if len(envs) != 50 {
		t.Fatalf("%d events, want 50", len(envs))
	}
}

func seqs(envs []Envelope) []int64 {
	var out []int64
	for _, e := range envs {
		out = append(out, e.Seq)
	}
	return out
}

func equal(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
