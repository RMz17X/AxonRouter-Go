package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/urfave/cli/v2"

	"axonrouter/proxy/internal/config"
	"axonrouter/proxy/internal/daemon"
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
// picker offers exactly that, plus Exit.
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
		default:
			return nil
		}
	}
}

// menuText renders the header plus the numbered items, with the live state
// of any background gateway in front of the choice so the pick is informed.
func menuText() string {
	state := "stopped"
	if pid := daemon.RunningPID(); pid != 0 {
		state = fmt.Sprintf("\x1b[32mrunning (pid %d)\x1b[0m", pid)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n  %s v%s (%s/%s)\n", binaryName(), updater.CurrentVersion, runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&b, "  Server: %s  [%s]\n\n", serverURL(), state)
	b.WriteString("  1) Start in this terminal (foreground)\n")
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
