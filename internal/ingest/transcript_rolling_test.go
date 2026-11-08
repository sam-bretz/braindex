package ingest

import (
	"reflect"
	"testing"

	"github.com/sam-bretz/braindex/internal/library"
)

func TestRollingTranscriptCollapsesRepaintAndCarryover(t *testing.T) {
	raw := `WEBVTT
Kind: captions
Language: en

00:00:00.000 --> 00:00:01.470 align:start position:0%
Find<00:00:00.240><c> a specific example.</c>

00:00:01.470 --> 00:00:01.480 align:start position:0%
Find a specific example.

00:00:01.480 --> 00:00:03.110 align:start position:0%
Find a specific example.
Then<00:00:01.760><c> ask a follow-up.</c>

00:00:03.110 --> 00:00:03.120 align:start position:0%
Then ask a follow-up.

00:00:03.120 --> 00:00:04.830 align:start position:0%
Then ask a follow-up.
That matters.

00:00:07.000 --> 00:00:09.000 align:start position:0%
That matters.

00:00:09.000 --> 00:00:11.000 align:start position:0%
<v Jo>Take a note.</v>
`
	want := []library.Passage{
		{Text: "Find a specific example. Then ask a follow-up. That matters.", Locator: "00:00:00.000–00:00:04.830"},
		{Text: "That matters.", Locator: "00:00:07.000–00:00:09.000"},
		{Text: "Take a note.", Locator: "00:00:09.000–00:00:11.000 (Jo)"},
	}
	assertCaptionPassages(t, raw, want)
}

func TestRollingTranscriptBoundsPassageDuration(t *testing.T) {
	raw := `WEBVTT

00:00.000 --> 00:12.000
Find<00:00.240><c> an example.</c>

00:12.000 --> 00:12.010
Find an example.

00:12.010 --> 00:24.000
Find an example.
Ask a question.

00:24.000 --> 00:26.000
Ask a question.
Write it down.
`
	want := []library.Passage{
		{Text: "Find an example. Ask a question.", Locator: "00:00.000–00:24.000"},
		{Text: "Write it down.", Locator: "00:24.000–00:26.000"},
	}
	assertCaptionPassages(t, raw, want)
}

func TestInlineWordTimingDoesNotCoalesceOrdinaryCues(t *testing.T) {
	raw := `WEBVTT

00:00:00.000 --> 00:00:02.000
<v Jo>Read<00:00:00.250><c> the source.</c></v>

00:00:02.000 --> 00:00:03.000
<v Jo>Read the source.</v>
`
	want := []library.Passage{
		{Text: "Read the source.", Locator: "00:00:00.000–00:00:02.000 (Jo)"},
		{Text: "Read the source.", Locator: "00:00:02.000–00:00:03.000 (Jo)"},
	}
	assertCaptionPassages(t, raw, want)
}

func assertCaptionPassages(t *testing.T, raw string, want []library.Passage) {
	t.Helper()
	got, err := transcriptPassages(raw)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("transcriptPassages() = %#v, %v, want %#v", got, err, want)
	}
}
