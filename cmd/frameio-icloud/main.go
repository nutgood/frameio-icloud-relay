// frameio-icloud is a self-hosted relay that pulls Frame.io Camera-to-Cloud
// uploads into the host Mac's Photos library (which iCloud Photos sync
// uploads to iCloud), then deletes the source from Frame.io.
//
// Architecture: one binary, multiple subcommands. `serve` is the
// long-running service that the LaunchAgent invokes. Everything else
// (`auth`, `install`, `status`, ...) is a one-shot CLI command.
package main

import (
	"fmt"
	"os"
)

const usage = `frameio-icloud — Frame.io → macOS Photos (iCloud) relay

Usage:
  frameio-icloud <command> [flags]

Commands:
  serve            Run the relay service. Invoked by the LaunchAgent.
  auth             Interactive OAuth login (one-time setup).
  install          Install + start the LaunchAgent on this Mac.
  uninstall        Stop + remove the LaunchAgent (keeps config + tokens).
  start            Start the LaunchAgent (if installed).
  stop             Stop the LaunchAgent.
  restart          Restart the LaunchAgent (launchctl kickstart -k).
  status           Print service status (queries the running service).
  logs             Tail the service log.
  config           Get/set/list config keys.
  test-pushover    Send a one-off Pushover notification using current config.
  test-photos      Verify the binary can drive Photos.app via AppleScript.
  version          Print version and exit.

Run "frameio-icloud <command> -h" for command-specific flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	switch cmd {
	case "serve":
		runServe(args)
	case "auth":
		runAuth(args)
	case "install":
		runInstall(args)
	case "uninstall":
		runUninstall(args)
	case "start":
		runStart(args)
	case "stop":
		runStop(args)
	case "restart":
		runRestart(args)
	case "status":
		runStatus(args)
	case "logs":
		runLogs(args)
	case "config":
		runConfig(args)
	case "test-pushover":
		runTestPushover(args)
	case "test-photos":
		runTestPhotos(args)
	case "version":
		fmt.Println("frameio-icloud " + version)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

// version is set at build time via -ldflags="-X main.version=...". Defaults
// to "dev" for local builds.
var version = "dev"
