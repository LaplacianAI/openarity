package secrets

import (
	"errors"
	"testing"
)

func TestStaticReturnsTheStoredSecret(t *testing.T) {
	t.Parallel()

	store := Static{"tenants/t1/channels/c1": "tok"}

	got, err := store.Get(t.Context(), "tenants/t1/channels/c1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "tok" {
		t.Errorf("Get = %q, want %q", got, "tok")
	}
}

// Fail closed: a missing path is ErrNotFound, never an empty string with a
// nil error — an empty string would sail into a signature check and pass or
// fail for the wrong reason.
func TestStaticMissingPathIsErrNotFound(t *testing.T) {
	t.Parallel()

	got, err := Static{}.Get(t.Context(), "tenants/t1/channels/nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get err = %v, want ErrNotFound", err)
	}
	if got != "" {
		t.Errorf("Get on a miss returned %q, want empty", got)
	}
}

// The zero value must behave like an empty store, not panic — main wires a
// bare Static{} until Vault exists.
func TestStaticZeroValueFailsClosed(t *testing.T) {
	t.Parallel()

	var store Static
	if _, err := store.Get(t.Context(), "any"); !errors.Is(err, ErrNotFound) {
		t.Errorf("zero-value Get err = %v, want ErrNotFound", err)
	}
}

// The path layout is a contract with everything already stored under it.
// Changing the format silently orphans every existing secret.
func TestChannelPathLayout(t *testing.T) {
	t.Parallel()

	if got := ChannelPath("t1", "c1"); got != "tenants/t1/channels/c1" {
		t.Errorf("ChannelPath = %q", got)
	}
}
