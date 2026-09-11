package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const serviceName = "grilled-cheese"

// installService registers this binary as a per-user service, so a server is
// already running whenever a session needs one. An agent cannot host the server
// itself: its processes die with each tool call, and its network namespace is
// unreachable from a browser.
//
// The unit points at this executable, so re-running after an upgrade is what
// moves the service onto the new binary.
func installService(args []string) error {
	workdir := flags("install-service", args, nil)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// launchd will not spawn a job whose log paths sit in a missing directory,
	// and serve's own MkdirAll comes too late for that.
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return outsideSandbox(err)
	}

	install := installSystemd
	if runtime.GOOS == "darwin" {
		install = installLaunchd
	}
	unit, logs, err := install(exe, workdir)
	if err != nil {
		return err
	}

	for range 20 {
		if b, err := os.ReadFile(addrPath(workdir)); err == nil {
			fmt.Printf("grilled cheese is serving http://%s\n", b)
			fmt.Printf("unit:    %s\n", unit)
			fmt.Printf("session: %s\n", workdir)
			fmt.Println("it starts automatically from now on; re-run this after an upgrade")
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("service started but published no address; check:\n  %s", logs)
}

// installSystemd writes a systemd user unit and starts it, returning the unit
// path and the command that shows why a failed start failed.
func installSystemd(exe, workdir string) (string, string, error) {
	logs := fmt.Sprintf("journalctl --user -u %s -n 20", serviceName)
	if _, err := exec.LookPath("systemctl"); err != nil {
		return "", logs, fmt.Errorf("no service manager on this machine — leave this running instead:\n  %s serve --workdir %s", exe, workdir)
	}

	unit := filepath.Join(configHome(), "systemd", "user", serviceName+".service")
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		return "", logs, outsideSandbox(err)
	}
	body := fmt.Sprintf(`[Unit]
Description=grilled cheese grilling UI

[Service]
ExecStart=%s serve --open=false --workdir %s
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
`, exe, workdir)
	if err := os.WriteFile(unit, []byte(body), 0o644); err != nil {
		return "", logs, outsideSandbox(err)
	}

	// restart is separate from enable --now: on a re-run after an upgrade the
	// service is already active, and enable alone would leave the old binary up.
	for _, argv := range [][]string{{"daemon-reload"}, {"enable", "--now", serviceName}, {"restart", serviceName}} {
		cmd := exec.Command("systemctl", append([]string{"--user"}, argv...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", logs, outsideSandbox(fmt.Errorf("systemctl --user %s: %s", strings.Join(argv, " "), bytes.TrimSpace(out)))
		}
	}
	return unit, logs, nil
}

// installLaunchd writes a launchd user agent and starts it, returning the plist
// path and the command that shows why a failed start failed. launchd keeps no
// journal of its own, so the job's output goes to a file in the workdir.
func installLaunchd(exe, workdir string) (string, string, error) {
	log := filepath.Join(workdir, "log")
	logs := "tail -n 20 " + log

	plist := filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", serviceName+".plist")
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		return "", logs, outsideSandbox(err)
	}
	// KeepAlive on anything but a clean exit is launchd's Restart=on-failure.
	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>serve</string>
		<string>--open=false</string>
		<string>--workdir</string>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
	<key>StandardOutPath</key><string>%s</string>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, serviceName, exe, workdir, log, log)
	if err := os.WriteFile(plist, []byte(body), 0o644); err != nil {
		return "", logs, outsideSandbox(err)
	}

	// bootstrap loads the plist as written, so booting the old job out first is
	// what moves an already-installed service onto the new binary.
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), serviceName)
	exec.Command("launchctl", "bootout", target).Run()
	cmd := exec.Command("launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), plist)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", logs, outsideSandbox(fmt.Errorf("launchctl bootstrap %s: %s", plist, bytes.TrimSpace(out)))
	}
	return plist, logs, nil
}

func configHome() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir
	}
	return filepath.Join(os.Getenv("HOME"), ".config")
}

// outsideSandbox names the one cause worth acting on: writing a unit and talking
// to the service manager both need access an agent's sandbox denies.
func outsideSandbox(err error) error {
	return fmt.Errorf("%w\n\nThis has to run outside an agent sandbox: ask the user to run it in their own terminal", err)
}
