package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestDemoUsesTwoCitedBrainsAndLeavesLibrariesAlone(t *testing.T) {
	temp := t.TempDir()
	ambient := t.TempDir()
	explicit := t.TempDir()
	t.Setenv("TMPDIR", temp)
	t.Setenv("BRAINDEX_LIBRARY", ambient)
	t.Setenv("BRAINDEX_OLLAMA_PORT", "not-a-port")

	want := strings.Join([]string{
		"Braindex demo with synthetic sources",
		"",
		"Question: How should I run founder-led sales discovery when budget tightening is slowing purchases?",
		"Topics matched: budgets, discovery",
		"",
		"[buying-climate] Buying Climate Brief, line 1",
		"  When budget tightening slows purchases, ask who can fund a small pilot this quarter.",
		"",
		"[field-notes] Discovery Field Notes, line 1",
		"  For founder-led sales discovery, open with the last real incident and ask what workaround the buyer used.",
		"",
		"Reopened source: Buying Climate Brief, line 1",
		"  When budget tightening slows purchases, ask who can fund a small pilot this quarter.",
		"",
	}, "\n")
	for _, args := range [][]string{{"demo"}, {"--library", explicit, "demo"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, nil, &stdout, &stderr); code != 0 || stderr.Len() != 0 {
			t.Fatalf("demo returned %d: %s", code, stderr.String())
		}
		if got := stdout.String(); got != want {
			t.Fatalf("demo output differs:\n%s", got)
		}
	}
	for _, path := range []string{temp, ambient, explicit} {
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != 0 {
			t.Fatalf("demo left files in %s: %v, %v", path, entries, err)
		}
	}

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"demo", "unexpected"}, nil, &stdout, &stderr); code == 0 ||
		stdout.Len() != 0 || !strings.Contains(stderr.String(), "usage: braindex demo") {
		t.Fatalf("unexpected demo args returned %d, stdout %q, stderr %q", code, &stdout, &stderr)
	}
}
