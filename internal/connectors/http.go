package connectors

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxPublicResponse = 8 << 20

func webURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("expected an HTTP or HTTPS URL without embedded credentials")
	}
	parsed.Fragment = ""
	return parsed, nil
}

func get(ctx context.Context, endpoint string, headers map[string]string, maxBytes int64) ([]byte, *url.URL, error) {
	return request(ctx, http.MethodGet, endpoint, headers, nil, maxBytes)
}

func request(ctx context.Context, method, endpoint string, headers map[string]string, body io.Reader, maxBytes int64) ([]byte, *url.URL, error) {
	client := http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(next *http.Request, prior []*http.Request) error {
			if method != http.MethodGet || len(headers) > 0 || len(prior) >= 5 {
				return http.ErrUseLastResponse
			}
			_, err := webURL(next.URL.String())
			return err
		},
	}
	return requestWithClient(ctx, &client, method, endpoint, headers, body, maxBytes)
}

func requestWithClient(ctx context.Context, client *http.Client, method, endpoint string, headers map[string]string, body io.Reader, maxBytes int64) ([]byte, *url.URL, error) {
	parsed, err := webURL(endpoint)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, parsed.String(), body)
	if err != nil {
		return nil, nil, err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	response, err := client.Do(req)
	if err != nil {
		var requestError *url.Error
		if errors.As(err, &requestError) {
			err = requestError.Err
		}
		return nil, nil, fmt.Errorf("%s %s failed: %w", method, parsed.Host, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, nil, fmt.Errorf("%s %s returned HTTP %d", method, parsed.Host, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, nil, fmt.Errorf("response from %s exceeds %d bytes", parsed.Host, maxBytes)
	}
	return data, response.Request.URL, nil
}

func link(base *url.URL, candidate string) string {
	candidate = strings.TrimSpace(candidate)
	parsed, err := url.Parse(candidate)
	if err != nil || candidate == "" {
		return ""
	}
	resolved, err := webURL(base.ResolveReference(parsed).String())
	if err != nil {
		return ""
	}
	return resolved.String()
}
