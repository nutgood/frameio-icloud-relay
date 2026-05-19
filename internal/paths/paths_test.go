package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestForUsesHome(t *testing.T) {
	home := "/tmp/fake-home"
	p := For(home)
	if !strings.HasPrefix(p.Support, home) {
		t.Errorf("Support not under home: %s", p.Support)
	}
	if !strings.HasPrefix(p.Plist, home) {
		t.Errorf("Plist not under home: %s", p.Plist)
	}
	if filepath.Base(p.Plist) != Label+".plist" {
		t.Errorf("plist filename: %s", filepath.Base(p.Plist))
	}
	if filepath.Base(p.InstalledBinary) != "frameio-icloud" {
		t.Errorf("binary name: %s", p.InstalledBinary)
	}
}

func TestEnsureDirs(t *testing.T) {
	home := t.TempDir()
	p := For(home)
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{p.Support, p.Downloads, p.LogDir} {
		info, err := os.Stat(d)
		if err != nil {
			t.Errorf("%s: %v", d, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", d)
		}
	}
}
