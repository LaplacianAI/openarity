package config

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
)

// The allowlist opens private ranges to discovery, for a team's self-hosted
// MCP servers. Each form here is one an operator would write.
func TestMCPPrivateNetworksAcceptsCIDRs(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{"OPENARITY_MCP_PRIVATE_NETWORKS": "10.0.0.0/8,172.16.0.0/12,127.0.0.0/8,fd00::/8,192.168.1.7/32"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	want := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("fd00::/8"),
		netip.MustParsePrefix("192.168.1.7/32"),
	}
	if !slices.Equal(cfg.MCPPrivateNetworks, want) {
		t.Errorf("MCPPrivateNetworks = %v, want %v", cfg.MCPPrivateNetworks, want)
	}
}

// Each refusal names the variable and says what to write instead. The
// trailing comma is here because the env library parses it, silently, into
// an invalid zero prefix — measured, not assumed.
//
// What does not parse at all fails in the env library before Validate runs,
// and its error names the Go field rather than the variable — as it does for
// every typed field in Config.
func TestMCPPrivateNetworksRefusesWhatItCannotMeanExactly(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]string{
		"10.0.0.0/8,":         "entry 1 is empty",
		"10.0.0.1/8":          "did you mean 10.0.0.0/8",
		"fd00::1/8":           "did you mean fd00::/8",
		"::ffff:10.0.0.0/104": "write it as IPv4",
		"10.0.0.0":            "MCPPrivateNetworks",
		"nonsense":            "MCPPrivateNetworks",
	} {
		_, err := load(map[string]string{"OPENARITY_MCP_PRIVATE_NETWORKS": raw})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("MCP_PRIVATE_NETWORKS=%q: err = %v, want it to say %q", raw, err, want)
		}
	}

	// Padding is not trimmed, so a space after a comma fails the boot rather
	// than becoming a range nobody wrote.
	if _, err := load(map[string]string{"OPENARITY_MCP_PRIVATE_NETWORKS": "10.0.0.0/8, 172.16.0.0/12"}); err == nil ||
		!strings.Contains(err.Error(), "MCPPrivateNetworks") {
		t.Errorf("a padded entry: err = %v, want a refusal", err)
	}
}

// Link-local is where cloud metadata answers with the instance's credentials.
// No entry may reach into it, including one that contains it — which is what
// makes "0.0.0.0/0, to just make it work" a boot failure.
func TestMCPPrivateNetworksNeverOpensLinkLocal(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]string{
		"169.254.0.0/16":                "overlaps 169.254.0.0/16",
		"169.254.169.254/32":            "overlaps 169.254.0.0/16",
		"169.0.0.0/8":                   "overlaps 169.254.0.0/16",
		"0.0.0.0/0":                     "overlaps 169.254.0.0/16",
		"fe80::/10":                     "overlaps fe80::/10",
		"fe80::1/128":                   "overlaps fe80::/10",
		"::/0":                          "overlaps fe80::/10",
		"10.0.0.0/8,169.254.169.254/32": "entry 169.254.169.254/32",
	} {
		_, err := load(map[string]string{"OPENARITY_MCP_PRIVATE_NETWORKS": raw})
		if err == nil || !strings.Contains(err.Error(), "MCP_PRIVATE_NETWORKS") || !strings.Contains(err.Error(), want) {
			t.Errorf("MCP_PRIVATE_NETWORKS=%q: err = %v, want a refusal saying %q", raw, err, want)
		}
	}
}

// Neighbours of link-local are not link-local: the check is an overlap, not a
// first-octet match.
func TestMCPPrivateNetworksBesideLinkLocalAreAccepted(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"169.253.0.0/16", "169.255.0.0/16", "fec0::/10", "fe00::/9"} {
		if _, err := load(map[string]string{"OPENARITY_MCP_PRIVATE_NETWORKS": raw}); err != nil {
			t.Errorf("MCP_PRIVATE_NETWORKS=%q: %v", raw, err)
		}
	}
}

func TestMCPPrivateNetworksAreCheckedInEveryEnvironment(t *testing.T) {
	t.Parallel()

	for _, env := range []string{"development", "production"} {
		_, err := load(map[string]string{"OPENARITY_ENVIRONMENT": env, "OPENARITY_MCP_PRIVATE_NETWORKS": "169.254.0.0/16"})
		if err == nil || !strings.Contains(err.Error(), "MCP_PRIVATE_NETWORKS") {
			t.Errorf("%s: err = %v", env, err)
		}
	}
}

// The ranges are printed: an operator reading the boot log should see which
// internal networks discovery may reach.
func TestStringShowsTheMCPPrivateNetworks(t *testing.T) {
	t.Parallel()

	cfg, err := load(map[string]string{"OPENARITY_MCP_PRIVATE_NETWORKS": "10.0.0.0/8,fd00::/8"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if want := "MCPPrivateNetworks:[10.0.0.0/8 fd00::/8]"; !strings.Contains(cfg.String(), want) {
		t.Errorf("String() = %s, want it to contain %q", cfg.String(), want)
	}
}

// .env.example suggests this for a server on the developer's machine, so it
// has to boot.
func TestTheDocumentedLoopbackExampleIsAccepted(t *testing.T) {
	t.Parallel()

	if _, err := load(map[string]string{"OPENARITY_MCP_PRIVATE_NETWORKS": "127.0.0.0/8,::1/128"}); err != nil {
		t.Errorf("the .env.example value is refused: %v", err)
	}
}
