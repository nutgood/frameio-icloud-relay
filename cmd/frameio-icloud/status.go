package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/nutgood/frameio-icloud/internal/ipc"
	"github.com/nutgood/frameio-icloud/internal/launchd"
	"github.com/nutgood/frameio-icloud/internal/paths"
)

func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "output raw JSON")
	_ = fs.Parse(args)

	p, err := paths.Default()
	if err != nil {
		log.Fatalf("paths: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := ipc.Fetch(ctx, p.Socket)
	if err != nil {
		if errors.Is(err, ipc.ErrNotRunning) {
			fmt.Println("service: NOT RUNNING")
			loaded, _ := launchd.Running()
			if loaded {
				fmt.Println("(LaunchAgent is loaded but the service isn't responding — try `frameio-icloud logs`)")
			} else if _, err := os.Stat(p.Plist); err == nil {
				fmt.Println("LaunchAgent plist present but not loaded. Run `frameio-icloud start`.")
			} else {
				fmt.Println("Run `frameio-icloud install` to install the LaunchAgent.")
			}
			return
		}
		log.Fatalf("status: %v", err)
	}
	if *jsonOut {
		printJSON(s)
		return
	}
	printHuman(s)
}

func printJSON(s *ipc.Status) {
	enc := jsonEncoder(os.Stdout)
	_ = enc.Encode(s)
}

func printHuman(s *ipc.Status) {
	fmt.Println("service:        RUNNING")
	fmt.Printf("pid:            %d\n", s.PID)
	fmt.Printf("uptime:         %s\n", time.Since(s.StartedAt).Round(time.Second))
	if s.AuthUser != "" {
		fmt.Printf("auth:           %s (token expires %s)\n", s.AuthUser, s.AuthExpiresAt.Format(time.RFC3339))
	}
	if s.PollingOnly {
		fmt.Printf("mode:           polling-only (every %s)\n", s.PollInterval)
	} else {
		fmt.Printf("mode:           webhook + reconcile (every %s)\n", s.PollInterval)
		fmt.Printf("webhook url:    %s\n", s.WebhookURL)
		fmt.Printf("webhook live:   %v\n", s.WebhookActive)
	}
	if len(s.InFlight) > 0 {
		fmt.Printf("in-flight:      %d (%v)\n", len(s.InFlight), s.InFlight)
	}
	if s.BurstOpen {
		fmt.Printf("burst:          open since %s — %d imported, %d failed\n",
			s.BurstStartedAt.Format(time.RFC3339), s.BurstCount, s.BurstFailed)
	}
	if len(s.RecentImports) > 0 {
		fmt.Println("recent imports:")
		for _, e := range s.RecentImports {
			fmt.Printf("  %s  %s\n", e.At.Format("15:04:05"), e.Message)
		}
	}
	if len(s.RecentErrors) > 0 {
		fmt.Println("recent errors:")
		for _, e := range s.RecentErrors {
			fmt.Printf("  %s  %s\n", e.At.Format("15:04:05"), e.Message)
		}
	}
}

func runLogs(args []string) {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	follow := fs.Bool("f", true, "follow (tail -f)")
	errOnly := fs.Bool("err", false, "tail the stderr log instead of stdout")
	_ = fs.Parse(args)

	p, err := paths.Default()
	if err != nil {
		log.Fatalf("paths: %v", err)
	}
	path := p.LogOut
	if *errOnly {
		path = p.LogErr
	}
	tailArgs := []string{"-n", "100"}
	if *follow {
		tailArgs = append(tailArgs, "-f")
	}
	tailArgs = append(tailArgs, path)
	cmd := exec.Command("tail", tailArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	_ = cmd.Run()
}
