//go:build !windows

package stack

// waitForStop is where the platforms differ.
//
// On Unix `oa stack stop` sends SIGTERM, which arrives through the context the
// root command installed, so there is nothing else to wait on. A nil channel
// blocks forever in a select, which is exactly right: the context is the only
// way this ever finishes here.
func waitForStop(_ string) (<-chan struct{}, func(), error) {
	return nil, func() {}, nil
}
