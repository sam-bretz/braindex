package ingest

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/sam-bretz/braindex/internal/library"
)

func imagePassages(data []byte) ([]library.Passage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "tesseract", "stdin", "stdout", "tsv")
	command.Stdin = bytes.NewReader(data)
	result, err := command.Output()
	if errors.Is(err, exec.ErrNotFound) {
		return nil, fmt.Errorf("image import requires Tesseract installed locally")
	}
	if err != nil {
		return nil, fmt.Errorf("run Tesseract: %w", err)
	}
	return ocrPassages(result)
}

func ocrPassages(data []byte) ([]library.Passage, error) {
	reader := bufio.NewScanner(bytes.NewReader(data))
	if !reader.Scan() || !strings.HasPrefix(reader.Text(), "level\tpage_num\t") {
		return nil, fmt.Errorf("missing Tesseract TSV header")
	}

	var passages []library.Passage
	var words []string
	var key, page string
	var left, top, right, bottom int
	flush := func() {
		if len(words) > 0 {
			passages = append(passages, library.Passage{
				Text: strings.Join(words, " "),
				Locator: fmt.Sprintf("image %s region x=%d y=%d width=%d height=%d",
					page, left, top, right-left, bottom-top),
			})
			words = nil
		}
	}

	for reader.Scan() {
		row := strings.SplitN(reader.Text(), "\t", 12)
		if len(row) != 12 {
			return nil, fmt.Errorf("unexpected Tesseract TSV record")
		}
		text := strings.TrimSpace(row[11])
		if row[0] != "5" || text == "" {
			continue
		}
		var region [4]int
		for i := range region {
			value, err := strconv.Atoi(row[i+6])
			if err != nil || value < 0 {
				return nil, fmt.Errorf("invalid Tesseract region")
			}
			region[i] = value
		}
		line := strings.Join(row[1:5], "/")
		x, y, width, height := region[0], region[1], region[2], region[3]
		if line != key {
			flush()
			key, page = line, row[1]
			left, top, right, bottom = x, y, x+width, y+height
		} else {
			left, top = min(left, x), min(top, y)
			right, bottom = max(right, x+width), max(bottom, y+height)
		}
		words = append(words, text)
	}
	flush()
	return passages, reader.Err()
}
