package index

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultOllamaPort = "11434"

type embedder struct {
	endpoint string
	client   *http.Client
}

func localEmbedder() (*embedder, error) {
	port := os.Getenv("BRAINDEX_OLLAMA_PORT")
	if port == "" {
		port = defaultOllamaPort
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port {
		return nil, errors.New("BRAINDEX_OLLAMA_PORT must be a local TCP port")
	}
	return &embedder{
		endpoint: "http://" + net.JoinHostPort("127.0.0.1", port),
		client: &http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
			},
			Timeout: 45 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (e *embedder) request(ctx context.Context, path string, payload any, out any) error {
	method := http.MethodGet
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body, method = bytes.NewReader(data), http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, e.endpoint+path, body)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("local Ollama %s: %w", path, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("local Ollama %s returned HTTP %d", path, res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 16<<20+1))
	if err != nil {
		return err
	}
	if len(data) > 16<<20 {
		return errors.New("Ollama response exceeds 16 MiB")
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("invalid Ollama %s response", path)
	}
	return nil
}

type embeddingModel struct {
	name, digest, family string
}

func (e *embedder) model(ctx context.Context, name string) (embeddingModel, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, "://") || strings.Contains(strings.ToLower(name), "cloud") {
		return embeddingModel{}, errors.New("select a locally installed Ollama embedding model")
	}
	if !strings.Contains(name[strings.LastIndex(name, "/")+1:], ":") {
		name += ":latest"
	}
	var tags struct {
		Models []struct {
			Name    string `json:"name"`
			Digest  string `json:"digest"`
			Size    int64  `json:"size"`
			Details struct {
				Format string `json:"format"`
				Family string `json:"family"`
			} `json:"details"`
			RemoteModel string `json:"remote_model"`
			RemoteHost  string `json:"remote_host"`
		} `json:"models"`
	}
	if err := e.request(ctx, "/api/tags", nil, &tags); err != nil {
		return embeddingModel{}, err
	}
	for _, tag := range tags.Models {
		if tag.Name != name {
			continue
		}
		digest, err := hex.DecodeString(tag.Digest)
		if err != nil || len(digest) != 32 || tag.Size <= 0 || tag.Details.Format != "gguf" || tag.RemoteModel != "" || tag.RemoteHost != "" {
			return embeddingModel{}, errors.New("Ollama model must be an installed local GGUF model")
		}
		return embeddingModel{name: name, digest: tag.Digest, family: tag.Details.Family}, nil
	}
	return embeddingModel{}, fmt.Errorf("Ollama model %q is not installed locally", name)
}

func (e *embedder) vectors(ctx context.Context, model embeddingModel, texts []string, query bool) ([][]float32, error) {
	if model.family == "nomic-bert" {
		prefix := "search_document: "
		if query {
			prefix = "search_query: "
		}
		input := make([]string, len(texts))
		for i, text := range texts {
			input[i] = prefix + text
		}
		texts = input
	}
	var response struct {
		Vectors [][]float32 `json:"embeddings"`
	}
	payload := struct {
		Model    string   `json:"model"`
		Input    []string `json:"input"`
		Truncate bool     `json:"truncate"`
	}{model.name, texts, false}
	if err := e.request(ctx, "/api/embed", payload, &response); err != nil {
		return nil, err
	}
	if len(response.Vectors) != len(texts) {
		return nil, errors.New("Ollama returned an unexpected embedding count")
	}
	for _, vector := range response.Vectors {
		if len(vector) == 0 || len(vector) > 8192 {
			return nil, errors.New("Ollama returned unsupported embedding dimensions")
		}
		var norm float64
		for _, value := range vector {
			norm += float64(value) * float64(value)
		}
		if norm == 0 || math.IsNaN(norm) || math.IsInf(norm, 0) {
			return nil, errors.New("Ollama returned an invalid embedding")
		}
		norm = math.Sqrt(norm)
		for i, value := range vector {
			vector[i] = float32(float64(value) / norm)
		}
	}
	return response.Vectors, nil
}
