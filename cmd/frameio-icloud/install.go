package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/nutgood/frameio-icloud/internal/launchd"
	"github.com/nutgood/frameio-icloud/internal/paths"
)

func runInstall(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	printPlist := fs.Bool("print-plist", false, "print the rendered plist and exit without installing")
	_ = fs.Parse(args)

	p, err := paths.Default()
	if err != nil {
		log.Fatalf("paths: %v", err)
	}
	if *printPlist {
		x, err := launchd.RenderPlist(p)
		if err != nil {
			log.Fatalf("render: %v", err)
		}
		fmt.Print(x)
		return
	}

	if err := p.EnsureDirs(); err != nil {
		log.Fatalf("ensure dirs: %v", err)
	}
	src, err := os.Executable()
	if err != nil {
		log.Fatalf("locate self: %v", err)
	}
	if err := launchd.Install(p, src); err != nil {
		log.Fatalf("install: %v", err)
	}
	fmt.Printf("installed LaunchAgent: %s\n", p.Plist)
	fmt.Printf("binary:                %s\n", p.InstalledBinary)
	fmt.Printf("logs:                  %s\n", p.LogOut)
	fmt.Println()
	fmt.Println("Service is now running. Check with `frameio-icloud status`.")
	fmt.Println()
	fmt.Println("If this is the first run, macOS will prompt for Automation permission")
	fmt.Println("for Photos.app the first time a photo arrives. Allow it, or imports")
	fmt.Println("will fail until you do.")
}

func runUninstall(_ []string) {
	p, err := paths.Default()
	if err != nil {
		log.Fatalf("paths: %v", err)
	}
	if err := launchd.Uninstall(p); err != nil {
		log.Fatalf("uninstall: %v", err)
	}
	fmt.Println("uninstalled. Config + tokens preserved at:")
	fmt.Println("  " + p.Support)
	fmt.Println("To remove those too, `rm -rf` that directory.")
}

func runStart(_ []string) {
	if err := launchd.Kickstart(); err != nil {
		log.Fatalf("start: %v", err)
	}
	fmt.Println("started.")
}

func runStop(_ []string) {
	if err := launchd.Stop(); err != nil {
		log.Fatalf("stop: %v", err)
	}
	fmt.Println("stopped.")
}

func runRestart(_ []string) {
	if err := launchd.Kickstart(); err != nil {
		log.Fatalf("restart: %v", err)
	}
	fmt.Println("restarted.")
}
