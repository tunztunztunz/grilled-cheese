package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const serviceName = "grilled-cheese"

// installService registers this binary as a systemd user service, so a server is
// already running whenever a session needs one. An agent cannot host the server
// itself: its processes die with each tool call, and its network namespace is
// unreachable from a browser.
//
// The unit points at this executable, so re-running after an upgrade is what
// moves the service onto the new binary.
func installService(args []string) error {
	workdir, err := flags("install-service", args, nil)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("no systemd on this machine — leave this running instead:\n  %s serve --workdir %s", exe, workdir)
	}

	unit := filepath.Join(configHome(), "systemd", "user", serviceName+".service")
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		return outsideSandbox(err)
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
		return outsideSandbox(err)
	}

	// restart is separate from enable --now: on a re-run after an upgrade the
	// service is already active, and enable alone would leave the old binary up.
	for _, argv := range [][]string{{"daemon-reload"}, {"enable", "--now", serviceName}, {"restart", serviceName}} {
		cmd := exec.Command("systemctl", append([]string{"--user"}, argv...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return outsideSandbox(fmt.Errorf("systemctl --user %s: %s", strings.Join(argv, " "), bytes.TrimSpace(out)))
		}
	}

	addrPath := filepath.Join(workdir, "addr")
	for range 20 {
		if b, err := os.ReadFile(addrPath); err == nil {
			fmt.Printf("grilled cheese is serving http://%s\n", b)
			fmt.Printf("unit:    %s\n", unit)
			fmt.Printf("session: %s\n", workdir)
			fmt.Println("it starts automatically from now on; re-run this after an upgrade")
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("service started but published no address; check:\n  journalctl --user -u %s -n 20", serviceName)
}

func configHome() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir
	}
	return filepath.Join(os.Getenv("HOME"), ".config")
}

// outsideSandbox names the one cause worth acting on: writing a unit and talking
// to systemd both need access an agent's sandbox denies.
func outsideSandbox(err error) error {
	return fmt.Errorf("%w\n\nThis has to run outside an agent sandbox — ask the user to run it in their own terminal.", err)
}
