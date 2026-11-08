package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLibraryCommands(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "existing.md")
	if err := os.WriteFile(file, []byte("keep this"), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (int, string) {
		t.Helper()
		var out, stderr bytes.Buffer
		code := Run(append([]string{"--library", root}, args...), strings.NewReader(""), &out, &stderr)
		return code, out.String() + stderr.String()
	}
	for _, args := range [][]string{{"init"}, {"init"}, {"brain", "create", "sales", "--name", "Founder Sales", "--kind", "topic"}} {
		if code, out := run(args...); code != 0 {
			t.Fatalf("%v: %s", args, out)
		}
	}
	if code, out := run("brain", "list", "--json"); code != 0 || !strings.Contains(out, "Founder Sales") {
		t.Fatalf("list: %d %s", code, out)
	}
	if code, _ := run("brain", "create", "../invalid"); code == 0 {
		t.Fatal("accepted invalid brain ID")
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "keep this" {
		t.Fatalf("existing file changed: %q, %v", data, err)
	}
}
