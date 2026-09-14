package stack

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const autostartLabel = "ai.laplacian.openarity"

type Unit struct {
	Path       string
	Content    string
	Register   []string
	Unregister []string
}

func UnitFor(goos, home, exe, root string) (Unit, error) {
	switch goos {
	case "darwin":
		return Unit{
			Path:       filepath.Join(home, "Library", "LaunchAgents", autostartLabel+".plist"),
			Content:    launchdPlist(exe, root),
			Register:   []string{"launchctl", "load", "-w", filepath.Join(home, "Library", "LaunchAgents", autostartLabel+".plist")},
			Unregister: []string{"launchctl", "unload", "-w", filepath.Join(home, "Library", "LaunchAgents", autostartLabel+".plist")},
		}, nil

	case "linux":
		return Unit{
			Path:       filepath.Join(home, ".config", "systemd", "user", "openarity.service"),
			Content:    systemdUnit(exe, root),
			Register:   []string{"systemctl", "--user", "enable", "--now", "openarity.service"},
			Unregister: []string{"systemctl", "--user", "disable", "--now", "openarity.service"},
		}, nil

	case "windows":
		return Unit{
			Register: []string{
				"schtasks", "/create", "/f",
				"/tn", "Openarity",
				"/tr", `"` + exe + `" stack start --root "` + root + `"`,
				"/sc", "ONLOGON",
			},
			Unregister: []string{"schtasks", "/delete", "/f", "/tn", "Openarity"},
		}, nil

	default:
		return Unit{}, fmt.Errorf("stack: no autostart mechanism for %s", goos)
	}
}

// KeepAlive is a dict rather than <true/> deliberately. Loading the agent
// runs it at once, which during setup means arriving at an install setup has
// already started — and `oa stack start` says so and exits 0. Plain KeepAlive
// restarts an agent whatever its exit status, so that clean exit would be
// restarted every ten seconds for as long as the other supervisor lived.
// SuccessfulExit false means "restart it only if it failed", which is the
// thing actually wanted: survive a crash, accept a deliberate stop.
func launchdPlist(exe, root string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + autostartLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + xmlEscape(exe) + `</string>
		<string>stack</string>
		<string>start</string>
		<string>--root</string>
		<string>` + xmlEscape(root) + `</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>` + xmlEscape(autostartLog(root)) + `</string>
	<key>StandardErrorPath</key>
	<string>` + xmlEscape(autostartLog(root)) + `</string>
</dict>
</plist>
`
}

// autostartLog is where a start nobody watched says what went wrong.
//
// Without it launchd throws the output away: an agent that exits 1 leaves a
// status in `launchctl list` and nothing else, so "the dashboard refuses to
// connect" has no next step. systemd keeps its own journal, which is why only
// launchd needs this.
func autostartLog(root string) string {
	return filepath.Join(root, "logs", "autostart.log")
}

func systemdUnit(exe, root string) string {
	return `[Unit]
Description=Openarity
After=network.target

[Service]
Type=simple
ExecStart="` + exe + `" stack start --root "` + root + `"
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func InstallAutostart(ctx context.Context, u Unit) error {
	if u.Path != "" {
		if err := os.MkdirAll(filepath.Dir(u.Path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(u.Path, []byte(u.Content), 0o600); err != nil {
			return err
		}
	}
	return runUnit(ctx, u.Register)
}

func RemoveAutostart(ctx context.Context, u Unit) error {
	err := runUnit(ctx, u.Unregister)

	if u.Path != "" {
		if removeErr := os.Remove(u.Path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return removeErr
		}
	}
	return err
}

func runUnit(ctx context.Context, argv []string) error {
	if len(argv) == 0 {
		return nil
	}

	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput() //nolint:gosec // argv is built by UnitFor, not from input
	if err != nil {
		return fmt.Errorf("stack: %s: %w\n%s", argv[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}
