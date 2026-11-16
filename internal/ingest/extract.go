package ingest

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/razvandimescu/gopdf/pdf"
	"github.com/sam-bretz/braindex/internal/library"
)

const (
	maxSourceBytes  = 64 << 20
	maxPDFPages     = 2000
	maxPDFTextBytes = 16 << 20
)

// Extract reads a supported local file and preserves passage locations.
func Extract(path string) (library.Source, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return library.Source{}, err
	}
	// Nonblocking open allows rejecting a named pipe before it can wait for a writer.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return library.Source{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return library.Source{}, err
	}
	if !info.Mode().IsRegular() {
		return library.Source{}, fmt.Errorf("%s: expected a regular source file", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSourceBytes+1))
	if err != nil {
		return library.Source{}, err
	}
	if len(data) > maxSourceBytes {
		return library.Source{}, fmt.Errorf("%s: source exceeds 64 MiB", path)
	}
	source := library.Source{Path: path, Title: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown", ".txt":
		if !utf8.Valid(data) {
			return library.Source{}, fmt.Errorf("%s: expected UTF-8 text", path)
		}
		source.Passages = LinePassages(strings.TrimPrefix(string(data), "\uFEFF"))
	case ".pdf":
		source.Passages, err = PDFPassages(data)
	case ".eml":
		source, err = ExtractEmail(data, path)
	case ".vtt", ".srt":
		source.Passages, err = transcriptPassages(string(data))
	case ".png", ".jpg", ".jpeg", ".gif", ".tif", ".tiff", ".webp", ".bmp":
		source.Passages, err = imagePassages(data)
	default:
		return library.Source{}, fmt.Errorf("%s: unsupported source format", path)
	}
	if err != nil {
		return library.Source{}, fmt.Errorf("%s: %w", path, err)
	}
	source.Status = TextStatus(source.Passages)
	return source, nil
}

// TextStatus describes whether an extracted source can be searched.
func TextStatus(passages []library.Passage) string {
	if len(passages) == 0 {
		return "unavailable"
	}
	return "ready"
}

// LinePassages splits text by paragraph and bounds long paragraphs while
// retaining one-based line numbers from the original source.
func LinePassages(text string) []library.Passage {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var passages []library.Passage
	var paragraph []string
	start := 0
	flush := func() {
		if len(paragraph) > 0 {
			passages = append(passages, library.Passage{
				Text: strings.Join(paragraph, "\n"), Locator: lineLocator(start, start+len(paragraph)-1),
			})
			paragraph = nil
		}
	}
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			flush()
			continue
		}
		if len(paragraph) == 12 {
			flush()
		}
		if len(paragraph) == 0 {
			start = i + 1
		}
		paragraph = append(paragraph, line)
	}
	flush()
	return passages
}

func lineLocator(start, end int) string {
	if start == end {
		return fmt.Sprintf("line %d", start)
	}
	return fmt.Sprintf("lines %d-%d", start, end)
}

// PDFPassages extracts page-aware text from a trusted local PDF. Its input and
// output limits do not bound the PDF parser's internal stream decoding.
func PDFPassages(data []byte) ([]library.Passage, error) {
	if len(data) > maxSourceBytes {
		return nil, fmt.Errorf("PDF exceeds %d MiB", maxSourceBytes>>20)
	}
	return readPDFPassages(data, maxPDFPages, maxPDFTextBytes)
}

func readPDFPassages(data []byte, maxPages, maxTextBytes int) (passages []library.Passage, err error) {
	// The parser can panic on corrupt stream offsets in untrusted documents.
	defer func() {
		if recovered := recover(); recovered != nil {
			passages, err = nil, fmt.Errorf("invalid PDF data: %v", recovered)
		}
	}()
	document, err := pdf.OpenBytes(data)
	if err != nil {
		return nil, fmt.Errorf("open PDF: %w", err)
	}
	pages := document.NumPages()
	if pages > maxPages {
		return nil, fmt.Errorf("PDF exceeds %d pages", maxPages)
	}
	textBytes := 0
	for i := 0; i < pages; i++ {
		text, err := document.Page(i).Text()
		if err != nil {
			return nil, fmt.Errorf("extract PDF page %d: %w", i+1, err)
		}
		textBytes += len(text)
		if textBytes > maxTextBytes {
			return nil, fmt.Errorf("PDF extracted text exceeds %d bytes", maxTextBytes)
		}
		for _, chunk := range LinePassages(text) {
			chunk.Locator = fmt.Sprintf("page %d, %s", i+1, chunk.Locator)
			passages = append(passages, chunk)
		}
	}
	return passages, nil
}
