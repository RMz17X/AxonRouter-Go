package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"
	"go.uber.org/fx"

	"9router/proxy/internal/app"
	"9router/proxy/internal/daemon"
	"9router/proxy/internal/shutdown"
	"9router/proxy/internal/updater"
)

// binaryName lets the same executable answer to `opencode-go`, so a renamed
// copy (or symlink) shows the matching name in help and errors.
func binaryName() string {
	if base := filepath.Base(os.Args[0]); strings.HasPrefix(base, "opencode-go") {
		return "opencode-go"
	}
	return "9router-go"
}

func main() {
	self := binaryName()
	app := &cli.App{
		Name:  self,
		Usage: "AI API proxy gateway with token saver features",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "rtk",
				Value: os.Getenv("RTK_ENABLED") != "false",
				Usage: "enable RTK input compression (env: RTK_ENABLED)",
			},
			&cli.BoolFlag{
				Name:  "caveman",
				Value: os.Getenv("CAVEMAN_ENABLED") == "true",
				Usage: "enable Caveman terse output style (env: CAVEMAN_ENABLED)",
			},
			&cli.BoolFlag{
				Name:  "ponytail",
				Value: os.Getenv("PONYTAIL_ENABLED") == "true",
				Usage: "enable Ponytail lazy dev code style (env: PONYTAIL_ENABLED)",
			},
			&cli.BoolFlag{
				Name:  "adhd",
				Value: os.Getenv("ADHD_ENABLED") == "true",
				Usage: "enable I have ADHD action-first output style (env: ADHD_ENABLED)",
			},
			&cli.BoolFlag{
				Name:  "auto-update",
				Value: os.Getenv("AUTO_UPDATE") == "true",
				Usage: "automatically download and install updates if available (env: AUTO_UPDATE)",
			},
			&cli.BoolFlag{
				Name:  "no-injection-guard",
				Value: os.Getenv("INJECTION_GUARD_DISABLED") == "true",
				Usage: "disable the prompt-injection detector (on by default; env: INJECTION_GUARD_DISABLED)",
			},
		},
		Commands: []*cli.Command{
			{
				Name:  "version",
				Usage: "Display version details and check for updates",
				Action: func(cCtx *cli.Context) error {
					info, err := updater.CheckUpdate(cCtx.Context)
					if err != nil {
						fmt.Printf("9router-go version %s (%s/%s)\nUpdate check failed: %v\n", updater.CurrentVersion, runtime.GOOS, runtime.GOARCH, err)
						return nil
					}
					fmt.Printf("9router-go version %s (%s/%s)\n", info.CurrentVersion, info.OS, info.Arch)
					fmt.Printf("Latest version: %s\n", info.LatestVersion)
					if info.HasUpdate {
						fmt.Printf("\n🚀 NEW UPDATE AVAILABLE! (%s)\nNotes: %s\nRun '9router-go update' to install.\n", info.LatestVersion, info.ReleaseNotes)
					} else {
						fmt.Println("App is up to date.")
					}
					return nil
				},
			},
			{
				Name:  "update",
				Usage: "Check and perform self-update to the latest version",
				Action: func(cCtx *cli.Context) error {
					fmt.Printf("Checking for updates (current: %s)...\n", updater.CurrentVersion)
					info, err := updater.CheckUpdate(cCtx.Context)
					if err != nil {
						return fmt.Errorf("update check failed: %w", err)
					}
					if !info.HasUpdate {
						fmt.Printf("9router-go is already on the latest version (%s).\n", info.CurrentVersion)
						return nil
					}
					fmt.Printf("Downloading update v%s...\n", info.LatestVersion)
					if err := updater.PerformSelfUpdate(info.DownloadURL, info.SHA256); err != nil {
						return fmt.Errorf("update failed: %w", err)
					}
					fmt.Println("✅ 9router-go updated successfully!")
					return nil
				},
			},
		},
		Action: defaultAction,
	}
	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}

// defaultAction is the flag-free entry point: the foreground server is the
// binary's only run mode. (The detached daemon and host-service subcommands
// were removed in P5 — Docker is the deployment surface now; the tray
// companion keeps its own lifecycle through internal/daemon.)
//
// Any positional argument reaching here matched no command, so it is a typo
// or a stale name — never boot the server on it. Booting on unknown input is
// what trapped users behind `bind: address already in use`: typing a
// renamed command while another gateway already held the port started a
// second gateway instead of running the intended subcommand.
func defaultAction(cCtx *cli.Context) error {
	if args := cCtx.Args(); args.Len() > 0 {
		return unknownCommandError(args.Slice())
	}
	// On a terminal, bare `9router-go` offers the interface picker; without one
	// (container, supervisor, pipe) it boots the server directly.
	if stdinIsTerminal() {
		return runMenu(cCtx)
	}
	return runServer(cCtx)
}

// unknownCommandError turns a missed command dispatch into guidance instead
// of a boot.
func unknownCommandError(args []string) error {
	return fmt.Errorf("unknown command %q — see '%s help'", args[0], binaryName())
}

func runServer(cCtx *cli.Context) error {
	if logPath := os.Getenv("LOG_FILE"); logPath != "" {
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			log.SetOutput(logFile)
			defer logFile.Close()
		} else {
			log.Printf("[config] warning: cannot open LOG_FILE=%s, using stderr: %v", logPath, err)
		}
	}

	cliParams := app.NewCLIParams(cCtx)

	fxApp := fx.New(
		app.AppModule,
		fx.Replace(cliParams),
		app.DefaultFxLogger(),
	)

	startCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := fxApp.Start(startCtx); err != nil {
		return err
	}

	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	// A process spawned as a daemon (by the tray companion, through
	// internal/daemon) records its PID so the spawner can find it after the
	// launching terminal is long gone. A plain foreground run is not marked.
	if daemon.IsBackgroundProcess() {
		daemon.RegisterPID()
		defer daemon.UnregisterPID()
	}

	select {
	case <-signals: // ^C or a supervisor's SIGTERM
	case <-shutdown.StopRequested():
		// The dashboard shutdown button and the updater's restart hook both
		// land here. Windows cannot raise SIGTERM on itself, so this request
		// channel is the only portable way to stop this process.
	}

	// A second signal force-quits immediately (e.g. a stream stuck mid-drain).
	go func() {
		<-signals
		fmt.Fprintln(os.Stdout, "\n  Force quitting...")
		os.Exit(1)
	}()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer stopCancel()
	stopErr := fxApp.Stop(stopCtx)

	// The listener is closed now, so a restart hook can bind the same port.
	shutdown.RunAfterStop()
	return stopErr
}
