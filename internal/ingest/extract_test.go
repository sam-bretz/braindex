package ingest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExtractLocal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.md")
	if err := os.WriteFile(path, []byte("# First\n\nSecond line\nThird line\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := Extract(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(source.Passages) != 2 || source.Passages[1].Locator != "lines 3-4" || source.Status != "ready" {
		t.Fatalf("unexpected Markdown source %+v", source)
	}
	if err := os.WriteFile(path, []byte(" \n"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err = Extract(path)
	if err != nil || source.Status != "unavailable" {
		t.Fatalf("empty source = %+v, %v", source, err)
	}
	if _, err := Extract(filepath.Join(dir, "missing.txt")); err == nil {
		t.Fatal("expected missing source to fail")
	}
}

func TestExtractPDFPageLocations(t *testing.T) {
	source, err := Extract("testdata/two-pages.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if len(source.Passages) != 2 {
		t.Fatalf("expected two PDF passages, got %+v", source.Passages)
	}
	if !strings.HasPrefix(source.Passages[0].Locator, "page 1,") || !strings.Contains(source.Passages[0].Text, "Discovery") ||
		!strings.HasPrefix(source.Passages[1].Locator, "page 2,") || !strings.Contains(source.Passages[1].Text, "customer response") {
		t.Fatalf("unexpected PDF passages %+v", source.Passages)
	}
}

func TestEmailPreservesIssueProvenance(t *testing.T) {
	message := strings.Join([]string{
		"From: Test Research <research@example.test>",
		"Subject: =?UTF-8?Q?Issue_42?=",
		"Date: Thu, 1 Jan 2026 12:00:00 -0500",
		"Message-ID: <issue42@example.test>",
		"MIME-Version: 1.0",
		"Content-Type: multipart/alternative; boundary=abc",
		"",
		"--abc",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: quoted-printable",
		"",
		"Evidence=20from issue 42.",
		"--abc",
		"Content-Type: text/html",
		"",
		"<p>Duplicated HTML</p>",
		"--abc--",
	}, "\r\n")
	source, err := ExtractEmail([]byte(message), "issue.eml")
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := ExtractEmail([]byte(message), "renamed.eml")
	if err != nil || duplicate.ID != source.ID || source.ID == "" ||
		source.Title != "Issue 42" || source.Author != "Test Research" || source.Date != "2026-01-01T12:00:00-05:00" ||
		len(source.Passages) != 1 || source.Passages[0].Text != "Evidence from issue 42." {
		t.Fatalf("email = %+v, duplicate = %+v, err = %v", source, duplicate, err)
	}
}

func TestTranscriptPreservesOnlyExplicitVoice(t *testing.T) {
	passages, err := transcriptPassages("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n<v Alex>Find a specific example.</v>\n\n00:00:02.000 --> 00:00:03.000\nThen ask a follow-up.")
	if err != nil || len(passages) != 2 || passages[0].Locator != "00:00:01.000–00:00:02.000 (Alex)" ||
		passages[0].Text != "Find a specific example." || strings.Contains(passages[1].Locator, "Alex") {
		t.Fatalf("unexpected transcript %+v, %v", passages, err)
	}
}

func TestOCRPreservesImageRegions(t *testing.T) {
	header := "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n"
	passages, err := ocrPassages([]byte(header + "5\t1\t1\t1\t1\t1\t12\t34\t56\t78\t96\tDiscovery\n"))
	if err != nil || len(passages) != 1 || passages[0].Text != "Discovery" || passages[0].Locator != "image 1 region x=12 y=34 width=56 height=78" {
		t.Fatalf("unexpected OCR %+v, %v", passages, err)
	}
}

func TestExtractImageLocally(t *testing.T) {
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("Tesseract is not installed")
	}
	source, err := Extract("testdata/discovery.png")
	if err != nil {
		t.Fatal(err)
	}
	if source.Status != "ready" || len(source.Passages) == 0 ||
		!strings.Contains(source.Passages[0].Text, "Customer discovery") ||
		!strings.HasPrefix(source.Passages[0].Locator, "image 1 region ") {
		t.Fatalf("unexpected image source %+v", source)
	}
}

func TestExtractRejectsNamedPipeWithoutWaitingForWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipe.txt")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := Extract(path)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "expected a regular source file") {
			t.Fatalf("expected a regular-file error, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("extract blocked on a named pipe with no writer")
	}
}
