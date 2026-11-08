package ingest

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"path/filepath"
	"strings"
	"time"

	"github.com/sam-bretz/braindex/internal/library"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// ExtractEmail extracts a saved message without opening attachments or links.
func ExtractEmail(data []byte, path string) (library.Source, error) {
	if len(data) > maxSourceBytes {
		return library.Source{}, fmt.Errorf("email exceeds 64 MiB")
	}
	message, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return library.Source{}, fmt.Errorf("read email: %w", err)
	}
	decoder := mime.WordDecoder{CharsetReader: charset.NewReaderLabel}
	decode := func(value string) string {
		text, err := decoder.DecodeHeader(value)
		if err != nil {
			return value
		}
		return text
	}
	source := library.Source{Path: path, Title: decode(message.Header.Get("Subject"))}
	if source.Title == "" && path != "" {
		source.Title = filepath.Base(path)
	}
	from := decode(message.Header.Get("From"))
	if address, err := mail.ParseAddress(from); err == nil {
		source.Author = address.Name
		if source.Author == "" {
			source.Author = address.Address
		}
	} else {
		source.Author = from
	}
	if date, err := message.Header.Date(); err == nil {
		source.Date = date.Format(time.RFC3339)
	}
	if id := strings.Trim(message.Header.Get("Message-ID"), " <>"); id != "" {
		source.ID = StableID("email", id)
	}
	plain, rich, err := emailText(mimeHeader(message.Header), message.Body, 0)
	if err != nil {
		return library.Source{}, err
	}
	if plain == "" {
		plain = rich
	}
	source.Passages = LinePassages(plain)
	for i := range source.Passages {
		source.Passages[i].Locator = "email body " + source.Passages[i].Locator
	}
	source.Status = TextStatus(source.Passages)
	return source, nil
}

type mimeHeader map[string][]string

func (h mimeHeader) get(key string) string {
	values := h[key]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func emailText(header mimeHeader, body io.Reader, depth int) (plain, rich string, err error) {
	if depth > 12 {
		return "", "", fmt.Errorf("email MIME parts are nested too deeply")
	}
	disposition, _, _ := mime.ParseMediaType(header.get("Content-Disposition"))
	if disposition == "attachment" {
		return "", "", nil
	}
	contentType := header.get("Content-Type")
	if contentType == "" {
		contentType = "text/plain"
	}
	kind, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", "", fmt.Errorf("invalid email content type: %w", err)
	}
	if strings.HasPrefix(kind, "multipart/") {
		if params["boundary"] == "" {
			return "", "", fmt.Errorf("email multipart content has no boundary")
		}
		reader := multipart.NewReader(body, params["boundary"])
		var plainParts, richParts []string
		for {
			part, err := reader.NextRawPart()
			if err == io.EOF {
				return strings.Join(plainParts, "\n\n"), strings.Join(richParts, "\n\n"), nil
			}
			if err != nil {
				return "", "", err
			}
			partPlain, partRich, err := emailText(mimeHeader(part.Header), part, depth+1)
			part.Close()
			if err != nil {
				return "", "", err
			}
			if partPlain != "" {
				plainParts = append(plainParts, partPlain)
			}
			if partRich != "" {
				richParts = append(richParts, partRich)
			}
		}
	}
	if kind != "text/plain" && kind != "text/html" {
		return "", "", nil
	}
	switch strings.ToLower(header.get("Content-Transfer-Encoding")) {
	case "base64":
		body = base64.NewDecoder(base64.StdEncoding, body)
	case "quoted-printable":
		body = quotedprintable.NewReader(body)
	}
	if label := params["charset"]; label != "" {
		body, err = charset.NewReaderLabel(label, body)
		if err != nil {
			return "", "", err
		}
	}
	if kind == "text/html" {
		_, text, err := HTMLText(body)
		return "", text, err
	}
	data, err := io.ReadAll(body)
	return strings.TrimSpace(string(data)), "", err
}

// HTMLText extracts visible text and a title from HTML without following links.
func HTMLText(input io.Reader) (title, body string, err error) {
	document, err := html.Parse(input)
	if err != nil {
		return "", "", err
	}
	var output strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		switch node.Type {
		case html.ElementNode:
			switch node.Data {
			case "title":
				if node.FirstChild != nil {
					title = strings.Join(strings.Fields(node.FirstChild.Data), " ")
				}
				return
			case "head":
				for child := node.FirstChild; child != nil; child = child.NextSibling {
					if child.Type == html.ElementNode && child.Data == "title" {
						walk(child)
						break
					}
				}
				return
			case "script", "style", "template", "noscript":
				return
			case "p", "div", "li", "br", "article", "section", "h1", "h2", "h3":
				output.WriteString("\n")
				defer output.WriteString("\n")
			}
		case html.TextNode:
			output.WriteString(strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(node.Data))
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	var lines []string
	for _, line := range strings.Split(output.String(), "\n") {
		if text := strings.Join(strings.Fields(line), " "); text != "" {
			lines = append(lines, text)
		}
	}
	return title, strings.Join(lines, "\n\n"), nil
}
