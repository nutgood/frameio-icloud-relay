// Package launchd renders the LaunchAgent plist for the relay and wraps
// the launchctl(1) commands used to install / uninstall / start / stop /
// kickstart it. All operations are scoped to the current user's gui/$UID
// domain — never system-wide.
package launchd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/nutgood/frameio-icloud-relay/internal/paths"
)

// plistTemplate is the minimum a LaunchAgent needs: an absolute program
// path, log redirects, RunAtLoad to start on user login, and KeepAlive
// scoped to crash recovery only (SuccessfulExit=false → don't restart on
// clean exit).
const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{{.Label}}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{.Binary}}</string>
        <string>serve</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>WorkingDirectory</key>
    <string>{{.Support}}</string>
    <key>StandardOutPath</key>
    <string>{{.LogOut}}</string>
    <key>StandardErrorPath</key>
    <string>{{.LogErr}}</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>HOME</key>
        <string>{{.Home}}</string>
        <key>PATH</key>
        <string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
    </dict>
</dict>
</plist>
`

type plistData struct {
	Label, Binary, Support, Home, LogOut, LogErr string
}

// RenderPlist returns the plist XML that would be written to disk for
// the given paths. Exposed for `frameio-icloud install --print-plist`
// dry-run / debugging.
func RenderPlist(p *paths.Paths) (string, error) {
	tmpl, err := template.New("plist").Parse(plistTemplate)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, plistData{
		Label:   paths.Label,
		Binary:  p.InstalledBinary,
		Support: p.Support,
		Home:    p.Home,
		LogOut:  p.LogOut,
		LogErr:  p.LogErr,
	}); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Install copies the running binary to InstalledBinary, writes the
// plist, and `launchctl bootstrap`s it into the user's gui domain so it
// starts immediately and again on every login.
//
// If a previous installation exists, it is `bootout`ed first (otherwise
// bootstrap refuses with EALREADY).
func Install(p *paths.Paths, sourceBinary string) error {
	if err := os.MkdirAll(filepath.Dir(p.InstalledBinary), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(p.LaunchAgentDir, 0o755); err != nil {
		return err
	}
	if err := copyFile(sourceBinary, p.InstalledBinary, 0o755); err != nil {
		return fmt.Errorf("copy binary: %w", err)
	}

	plistXML, err := RenderPlist(p)
	if err != nil {
		return err
	}
	tmp := p.Plist + ".tmp"
	if err := os.WriteFile(tmp, []byte(plistXML), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p.Plist); err != nil {
		return err
	}

	// Tear down any prior registration first — bootstrap is not
	// idempotent. Ignore exit status; common case is "not loaded".
	_ = bootout(p.Plist)
	if out, err := bootstrap(p.Plist); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s", err, out)
	}
	return nil
}

// Uninstall is the reverse of Install: bootout, remove plist, remove
// the installed binary (but never the support / state / log dirs — the
// user's tokens and config survive an uninstall).
func Uninstall(p *paths.Paths) error {
	_ = bootout(p.Plist) // tolerate "not loaded"
	if err := os.Remove(p.Plist); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(p.InstalledBinary); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Kickstart restarts the running service via launchctl kickstart -k.
func Kickstart() error {
	target, err := domainTarget()
	if err != nil {
		return err
	}
	out, err := runLaunchctl("kickstart", "-k", target+"/"+paths.Label)
	if err != nil {
		return fmt.Errorf("launchctl kickstart: %v: %s", err, out)
	}
	return nil
}

// Stop terminates the running service (launchd will restart it on its
// next trigger / on next login — this is "stop the current process",
// not "disable the agent").
func Stop() error {
	target, err := domainTarget()
	if err != nil {
		return err
	}
	out, err := runLaunchctl("kill", "SIGTERM", target+"/"+paths.Label)
	if err != nil {
		return fmt.Errorf("launchctl kill: %v: %s", err, out)
	}
	return nil
}

// Running reports whether the LaunchAgent is currently registered with
// launchd. False does not mean the binary is absent — only that it's
// not loaded.
func Running() (bool, error) {
	target, err := domainTarget()
	if err != nil {
		return false, err
	}
	out, err := runLaunchctl("print", target+"/"+paths.Label)
	if err != nil {
		// `launchctl print` exits non-zero when the service isn't
		// loaded. Distinguish that from a real error.
		if strings.Contains(out, "Could not find service") || strings.Contains(out, "service not found") {
			return false, nil
		}
		return false, nil
	}
	return strings.Contains(out, paths.Label), nil
}

func bootstrap(plistPath string) (string, error) {
	target, err := domainTarget()
	if err != nil {
		return "", err
	}
	return runLaunchctl("bootstrap", target, plistPath)
}

func bootout(plistPath string) error {
	target, err := domainTarget()
	if err != nil {
		return err
	}
	_, err = runLaunchctl("bootout", target, plistPath)
	return err
}

// domainTarget is "gui/<uid>" — the per-user GUI launchd domain. We
// require a GUI session because the relay drives Photos.app via
// AppleScript.
func domainTarget() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return "gui/" + u.Uid, nil
}

func runLaunchctl(args ...string) (string, error) {
	cmd := exec.Command("launchctl", args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
