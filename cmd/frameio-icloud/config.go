package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/nutgood/frameio-icloud-relay/internal/config"
	"github.com/nutgood/frameio-icloud-relay/internal/paths"
)

const configUsage = `Usage:
  frameio-icloud config list                  # print all keys + values
  frameio-icloud config path                  # print config file path
  frameio-icloud config get <key>             # print one value
  frameio-icloud config set <key> <value>     # set one value
  frameio-icloud config unset <key>           # clear one value

Keys:
  frameio.account     frameio.workspace     frameio.folder
  public_url          webhook_addr          poll_interval     stuck_timeout
  pushover.token      pushover.user_key
`

func runConfig(args []string) {
	if len(args) == 0 {
		fmt.Print(configUsage)
		os.Exit(2)
	}
	p, err := paths.Default()
	if err != nil {
		log.Fatalf("paths: %v", err)
	}
	cfg, err := config.Load(p.Config)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	switch args[0] {
	case "list":
		r := cfg.Redacted()
		_ = jsonEncoder(os.Stdout).Encode(r)
	case "path":
		fmt.Println(p.Config)
	case "get":
		if len(args) != 2 {
			log.Fatal("usage: frameio-icloud config get <key>")
		}
		v, err := cfg.Get(args[1])
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(v)
	case "set":
		if len(args) != 3 {
			log.Fatal("usage: frameio-icloud config set <key> <value>")
		}
		if err := cfg.Set(args[1], args[2]); err != nil {
			log.Fatal(err)
		}
		if err := config.Save(p.Config, cfg); err != nil {
			log.Fatalf("save: %v", err)
		}
		fmt.Printf("set %s\n", args[1])
	case "unset":
		if len(args) != 2 {
			log.Fatal("usage: frameio-icloud config unset <key>")
		}
		if err := cfg.Set(args[1], ""); err != nil {
			log.Fatal(err)
		}
		if err := config.Save(p.Config, cfg); err != nil {
			log.Fatalf("save: %v", err)
		}
		fmt.Printf("unset %s\n", args[1])
	default:
		fmt.Print(configUsage)
		os.Exit(2)
	}
}

// jsonEncoder is a tiny helper that returns a pretty-printing encoder.
func jsonEncoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc
}
