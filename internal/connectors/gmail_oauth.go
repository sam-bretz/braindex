package connectors

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	gmailReadonly = "https://www.googleapis.com/auth/gmail.readonly"
	googleConsent = "https://accounts.google.com/o/oauth2/v2/auth"
	googleToken   = "https://oauth2.googleapis.com/token"
)

// AuthorizeGmail uses the Desktop app flow with a loopback callback and PKCE.
func AuthorizeGmail(ctx context.Context, clientID, clientSecret string, showURL func(string) error) (string, error) {
	return authorizeGmail(ctx, googleConsent, googleToken, clientID, clientSecret, showURL)
}

func authorizeGmail(ctx context.Context, consent, tokenEndpoint, clientID, clientSecret string, showURL func(string) error) (string, error) {
	if strings.TrimSpace(clientID) == "" {
		return "", errors.New("Gmail Desktop OAuth client ID is required")
	}
	state := randomURLToken()
	verifier := randomURLToken()
	challenge := sha256.Sum256([]byte(verifier))

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("start Gmail OAuth loopback callback: %w", err)
	}
	redirect := "http://" + listener.Addr().String() + "/oauth/callback"
	results := make(chan string, 1)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/oauth/callback" {
			http.NotFound(w, r)
			return
		}
		values := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(values.Get("state")), []byte(state)) != 1 {
			http.Error(w, "Invalid authorization state.", http.StatusBadRequest)
			return
		}
		code := values.Get("code")
		if code == "" && values.Get("error") == "" {
			http.Error(w, "Missing authorization response.", http.StatusBadRequest)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "Authorization response received. You can return to Braindex.")
		select {
		case results <- code:
		default:
		}
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	params := url.Values{
		"client_id": {clientID}, "redirect_uri": {redirect}, "response_type": {"code"},
		"scope": {gmailReadonly}, "state": {state}, "access_type": {"offline"}, "prompt": {"consent"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"},
	}
	if err := showURL(consent + "?" + params.Encode()); err != nil {
		return "", err
	}
	select {
	case code := <-results:
		if code == "" {
			return "", errors.New("Gmail authorization was declined")
		}
		form := url.Values{
			"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code},
			"redirect_uri": {redirect}, "code_verifier": {verifier},
		}
		if clientSecret != "" {
			form.Set("client_secret", clientSecret)
		}
		data, _, err := request(ctx, http.MethodPost, tokenEndpoint, map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
		}, strings.NewReader(form.Encode()), 64<<10)
		if err != nil {
			return "", fmt.Errorf("exchange Gmail authorization code: %w", err)
		}
		var token struct {
			Refresh string `json:"refresh_token"`
			Scope   string `json:"scope"`
		}
		if err := json.Unmarshal(data, &token); err != nil || token.Refresh == "" {
			return "", errors.New("Google did not return a refresh token")
		}
		for _, scope := range strings.Fields(token.Scope) {
			if scope == gmailReadonly {
				return token.Refresh, nil
			}
		}
		return "", errors.New("Google did not grant read-only Gmail access")
	case <-ctx.Done():
		return "", fmt.Errorf("wait for Gmail consent: %w", ctx.Err())
	case err := <-done:
		return "", fmt.Errorf("Gmail OAuth callback stopped: %w", err)
	}
}

func randomURLToken() string {
	value := make([]byte, 32)
	rand.Read(value)
	return base64.RawURLEncoding.EncodeToString(value)
}

type GmailLabel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

func GmailLabels(ctx context.Context, token string) ([]GmailLabel, error) {
	return gmailLabels(ctx, "https://gmail.googleapis.com/gmail/v1/users/me/labels", token)
}

func gmailLabels(ctx context.Context, endpoint, token string) ([]GmailLabel, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("Gmail access token is required")
	}
	data, _, err := get(ctx, endpoint, map[string]string{"Authorization": "Bearer " + token}, maxPublicResponse)
	if err != nil {
		return nil, err
	}
	var response struct {
		Labels []GmailLabel `json:"labels"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("read Gmail labels: %w", err)
	}
	if response.Labels == nil {
		response.Labels = []GmailLabel{}
	}
	return response.Labels, nil
}
