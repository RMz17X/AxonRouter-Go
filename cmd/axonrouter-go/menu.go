package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/urfave/cli/v2"

	"axonrouter/proxy/internal/config"
	"axonrouter/proxy/internal/daemon"
	"axonrouter/proxy/internal/proc"
	"axonrouter/proxy/internal/updater"
)

// stdinIsTerminal reports whether the bare invocation has a human at the
// keyboard. A service unit, a container, or a pipe runs the same command with
// no TTY, and those must keep booting the server exactly as before.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// runMenu is the bare-`axonrouter-go` entry point on a terminal: the same
// interface picker the Node CLI shows instead of silently booting a server.
// Since P5 the foreground server is the binary's only run mode, so the
// picker offers exactly that, plus the tray companion (which manages its
// own gateway through internal/daemon, outside this binary's surface).
func runMenu(cCtx *cli.Context) error {
	in := bufio.NewReader(os.Stdin)
	first := true
	for {
		fmt.Print(menuText())
		line, ok := readChoice(in)
		if !ok {
			// The read ended at EOF before a byte arrived, so there is nobody
			// behind it however the stdin stat looked. /dev/null and a closed
			// pipe are both character devices, which is why stdinIsTerminal
			// let us in — and why a supervisor or the e2e harness, which give
			// the process exactly that, ended up here. That is the
			// non-interactive case, so boot the server as it always did rather
			// than exiting 1 with a menu nobody read.
			if first {
				return runServer(cCtx)
			}
			return nil
		}
		first = false
		choice := parseChoice(line)
		switch choice {
		case "1", "3":
			return foregroundStart(cCtx)
		case "2", "6":
			return startTray(cCtx)
		default:
			return nil
		}
	}
}

// menuText renders the header plus the numbered items, with the live state
// of any tray-managed gateway in front of the choice so the pick is informed.
func menuText() string {
	state := "stopped"
	if pid := daemon.RunningPID(); pid != 0 {
		state = fmt.Sprintf("\x1b[32mrunning (pid %d)\x1b[0m", pid)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n  %s v%s (%s/%s)\n", binaryName(), updater.CurrentVersion, runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "  Server: %s  [%s]\n\n", serverURL(), state)
	b.WriteString("  1) Start in this terminal (foreground)\n")
	b.WriteString("  2) Run in background with tray icon (frees the terminal)\n")
	b.WriteString("  0) Exit\n\n")
	b.WriteString("  Choose [1]: ")
	return b.String()
}

// foregroundStart boots the server in this terminal, printing the dashboard
// URL first so the operator can open it while the server runs.
func foregroundStart(cCtx *cli.Context) error {
	fmt.Printf("  Dashboard: %s/dashboard\n", serverURL())
	fmt.Println("  Starting in this terminal — Ctrl+C to stop.")
	return runServer(cCtx)
}

// serverURL is the address the gateway is expected to answer on.
//
// The port comes from the same resolved config the server binds, not from
// os.Getenv. A deployment that sets PORT in .env — the common case, and every
// compose file — was probed on 20130 while the server bound its real port.
func serverURL() string {
	cfg := config.LoadConfig()
	if cfg.Port <= 0 {
		return "http://127.0.0.1:20130"
	}
	port := strconv.Itoa(cfg.Port)
	// BIND_ADDR defaults to a wildcard bind, which is not dialable; only an
	// explicit loopback address is used to build the URL.
	if host := strings.TrimSpace(cfg.Host); host != "" && !isWildcardHost(host) {
		return "http://" + joinHostPort(host, port)
	}
	return "http://127.0.0.1:" + port
}

// isWildcardHost reports whether a bind address accepts every interface, in
// which case the URL would target 0.0.0.0 rather than the local listener.
func isWildcardHost(host string) bool {
	switch host {
	case "0.0.0.0", "::", "[::]", "*":
		return true
	}
	return false
}

// joinHostPort wraps bare IPv6 literals in brackets.
func joinHostPort(host, port string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

// readChoice blocks for one line. ok is false when the stream ended without
// yielding anything: the caller distinguishes "the operator pressed Ctrl+Z" from
// "there was never an operator" by whether this was the first read.
func readChoice(in *bufio.Reader) (line string, ok bool) {
	line, err := in.ReadString('\n')
	if err != nil && err != io.EOF {
		return "0", true
	}
	// io.EOF with a partial line (a terminal that closed mid-line) still
	// carries the operator's input; EOF with nothing does not.
	return line, line != ""
}

// parseChoice maps the typed line to a menu key. Enter alone takes the
// default (open dashboard); anything unrecognised exits rather than looping.
func parseChoice(line string) string {
	s := strings.TrimSpace(line)
	if s == "" {
		return "1"
	}
	switch s {
	case "0", "1", "2", "3", "4", "5", "6":
		return s
	case "q", "Q", "exit":
		return "0"
	}
	return "0"
}

// startTray hands the tray to its own binary and returns, so the terminal is
// free while the icon stays.
//
// The tray lives in cmd/axonrouter-tray because a GUI toolkit cannot be linked
// into this binary without breaking the CGO_ENABLED=0 cross-build for all five
// targets (docs/specs/tray-companion-architecture.md §X2). Spawning keeps the
// menu entry the Node CLI has ("Hide to Tray") without importing it here.
//
// Only the sibling binary is tried: a PATH lookup could pick up a different
// install's tray, which would then talk to a gateway that is not this one.
func startTray(cCtx *cli.Context) error {
	self, err := proc.SelfExecutable()
	if err != nil {
		return fmt.Errorf("tray: locate this binary: %w", err)
	}
	exe := filepath.Join(filepath.Dir(self), trayBinaryName())
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if _, err := os.Stat(exe); err != nil {
		fmt.Printf("tray companion %s not found next to %s; starting in this terminal instead...\n", filepath.Base(exe), filepath.Base(self))
		return foregroundStart(cCtx)
	}
	// CommandContext with a context that is never cancelled, not a deadline:
	// this process is released immediately below and is meant to outlive the
	// terminal, so a deadline here would kill the tray it just started.
	cmd := exec.CommandContext(context.Background(), exe, "--port", strconv.Itoa(config.LoadConfig().Port))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("tray: start %s: %w", filepath.Base(exe), err)
	}
	// Releasing lets the tray outlive this terminal, the same reason the
	// background daemon is released rather than waited on.
	_ = cmd.Process.Release()
	fmt.Printf("Tray started. Right-click the icon to open the dashboard or quit.\n")
	return nil
}

// trayBinaryName follows the same rename rule as binaryName, so an
// `opencode-go` install looks for `opencode-go-tray`.
func trayBinaryName() string {
	return binaryName() + "-tray"
}
