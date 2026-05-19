package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := &Config{
		FrameioAccount:  "acct-1",
		PublicURL:       "https://example.com/webhook",
		PushoverToken:   "tok",
		PushoverUserKey: "user",
		PollInterval:    "60s",
	}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.FrameioAccount != "acct-1" || loaded.PublicURL != "https://example.com/webhook" {
		t.Errorf("roundtrip: %+v", loaded)
	}
}

func TestLoadMissing(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if c.FrameioAccount != "" {
		t.Error("expected empty config")
	}
}

func TestGetSet(t *testing.T) {
	c := &Config{}
	if err := c.Set("public_url", "https://x"); err != nil {
		t.Fatal(err)
	}
	v, err := c.Get("public_url")
	if err != nil {
		t.Fatal(err)
	}
	if v != "https://x" {
		t.Errorf("get: %q", v)
	}
}

func TestSetUnknownKey(t *testing.T) {
	c := &Config{}
	if err := c.Set("does.not.exist", "x"); err == nil {
		t.Fatal("expected unknown-key error")
	}
}

func TestRedacted(t *testing.T) {
	c := &Config{
		FrameioAccount:  "acct",
		PushoverToken:   "secret-token",
		PushoverUserKey: "secret-user",
	}
	r := c.Redacted()
	if r.FrameioAccount != "acct" {
		t.Errorf("account should pass through")
	}
	if r.PushoverToken != "<set>" || r.PushoverUserKey != "<set>" {
		t.Errorf("secrets not redacted: %+v", r)
	}
	if c.PushoverToken != "secret-token" {
		t.Error("Redacted mutated the source struct")
	}
}

func TestValidKeysIncludesAll(t *testing.T) {
	c := &Config{}
	keys := c.ValidKeys()
	joined := strings.Join(keys, ",")
	for _, want := range []string{"frameio.account", "pushover.token", "public_url"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ValidKeys missing %q: %v", want, keys)
		}
	}
}
