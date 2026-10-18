package agent

import "testing"

func TestAModelErrorSaysItsStatusAndCode(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		err  ModelError
		want string
	}{
		{ModelError{Status: 429, Code: "insufficient_quota"}, "the model endpoint answered 429 Too Many Requests (insufficient_quota)"},
		{ModelError{Status: 502}, "the model endpoint answered 502 Bad Gateway"},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("Error() = %q, want %q", got, tc.want)
		}
	}
}
