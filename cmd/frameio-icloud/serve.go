package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nutgood/frameio-icloud/internal/config"
	"github.com/nutgood/frameio-icloud/internal/frameio"
	"github.com/nutgood/frameio-icloud/internal/paths"
	"github.com/nutgood/frameio-icloud/internal/photos"
	"github.com/nutgood/frameio-icloud/internal/pushover"
	"github.com/nutgood/frameio-icloud/internal/service"
)

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "download but skip Photos import + Frame.io delete")
	_ = fs.Parse(args)

	p, err := paths.Default()
	if err != nil {
		log.Fatalf("paths: %v", err)
	}
	if err := p.EnsureDirs(); err != nil {
		log.Fatalf("ensure dirs: %v", err)
	}

	cfg, err := config.Load(p.Config)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	tokens, err := frameio.LoadTokenStore(p.Tokens)
	if err != nil {
		log.Fatalf("tokens: %v", err)
	}
	if tokens.ClientID == "" || tokens.RefreshToken == "" {
		log.Fatalf("tokens file %s is incomplete — run `frameio-icloud auth` first", p.Tokens)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	opts := service.Options{
		Paths:        p,
		AccountID:    cfg.FrameioAccount,
		WorkspaceID:  cfg.FrameioWorkspace,
		FolderID:     cfg.FrameioFolder,
		PublicURL:    cfg.PublicURL,
		WebhookAddr:  cfg.WebhookAddr,
		PollInterval: parseDuration(cfg.PollInterval, 60*time.Second),
		StuckTimeout: parseDuration(cfg.StuckTimeout, 0),
		DryRun:       *dryRun,
		Photos:       photos.New(),
	}
	if cfg.PushoverToken != "" && cfg.PushoverUserKey != "" {
		opts.Pushover = pushover.New(cfg.PushoverToken, cfg.PushoverUserKey)
	}

	svc := service.New(tokens, opts)
	if err := svc.Run(ctx); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

func parseDuration(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		log.Printf("warn: invalid duration %q; using %s", s, fallback)
		return fallback
	}
	return d
}
