package launchd

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/nutgood/frameio-icloud/internal/paths"
)

func TestRenderPlist(t *testing.T) {
	p := paths.For("/Users/test")
	out, err := RenderPlist(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		paths.Label,
		"/Users/test/.local/bin/frameio-icloud",
		"<string>serve</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"<key>SuccessfulExit</key>",
		"/Users/test/Library/Application Support/frameio-icloud",
		"/Users/test/Library/Logs/frameio-icloud/frameio-icloud.log",
		"/Users/test/Library/Logs/frameio-icloud/frameio-icloud.err",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered plist missing %q\n---\n%s", want, out)
		}
	}
}

// TestPlistIsValid runs `plutil -lint` against the rendered plist if
// plutil is available (it is on every macOS host; the test is skipped
// elsewhere).
func TestPlistIsValid(t *testing.T) {
	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("plutil not available")
	}
	p := paths.For(t.TempDir())
	out, err := RenderPlist(p)
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir() + "/test.plist"
	if err := writeFile(tmp, out); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("plutil", "-lint", tmp)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("plutil -lint: %v\n%s", err, b)
	}
}

func writeFile(path, contents string) error {
	cmd := exec.Command("sh", "-c", "cat > "+path)
	cmd.Stdin = strings.NewReader(contents)
	return cmd.Run()
}
