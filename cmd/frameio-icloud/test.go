package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/nutgood/frameio-icloud-relay/internal/config"
	"github.com/nutgood/frameio-icloud-relay/internal/paths"
	"github.com/nutgood/frameio-icloud-relay/internal/photos"
	"github.com/nutgood/frameio-icloud-relay/internal/pushover"
)

func runTestPushover(args []string) {
	fs := flag.NewFlagSet("test-pushover", flag.ExitOnError)
	msg := fs.String("msg", "test from frameio-icloud", "message body")
	_ = fs.Parse(args)
	p, err := paths.Default()
	if err != nil {
		log.Fatalf("paths: %v", err)
	}
	cfg, err := config.Load(p.Config)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.PushoverToken == "" || cfg.PushoverUserKey == "" {
		log.Fatal("pushover.token / pushover.user_key not set in config")
	}
	c := pushover.New(cfg.PushoverToken, cfg.PushoverUserKey)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Send(ctx, pushover.Message{Title: "Frame.io", Body: *msg}); err != nil {
		log.Fatalf("pushover: %v", err)
	}
	fmt.Println("sent.")
}

func runTestPhotos(args []string) {
	fs := flag.NewFlagSet("test-photos", flag.ExitOnError)
	imagePath := fs.String("file", "", "image file to import (default: write a 1x1 PNG to a temp file)")
	_ = fs.Parse(args)

	importer := photos.New()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := importer.Check(ctx); err != nil {
		log.Fatalf("check: %v", err)
	}
	fmt.Println("Photos.app reachable via AppleScript.")

	if *imagePath == "" {
		tmp, err := writeTestPNG()
		if err != nil {
			log.Fatalf("write test png: %v", err)
		}
		defer os.Remove(tmp)
		*imagePath = tmp
		fmt.Printf("importing test image: %s\n", *imagePath)
	}
	if err := importer.Import(ctx, *imagePath); err != nil {
		log.Fatalf("import: %v", err)
	}
	fmt.Println("imported. Check Photos.app to confirm it landed.")
}

// writeTestPNG drops a 1x1 black PNG into a temp file. Smallest valid PNG
// payload — bytes were generated once by hand and verified against a
// PNG decoder. Used by `test-photos` so the user doesn't need a separate
// image file lying around.
func writeTestPNG() (string, error) {
	// 1x1 black PNG (67 bytes).
	png := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
		0x89, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
		0x42, 0x60, 0x82,
	}
	dir := os.TempDir()
	name := filepath.Join(dir, fmt.Sprintf("frameio-icloud-test-%d.png", time.Now().UnixNano()))
	if err := os.WriteFile(name, png, 0o644); err != nil {
		return "", err
	}
	return name, nil
}
