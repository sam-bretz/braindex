package connectors

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestGmailDesktopConsent(t *testing.T) {
	var auth url.Values
	tokenEndpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		verifier := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if r.Method != http.MethodPost || r.Form.Get("grant_type") != "authorization_code" ||
			r.Form.Get("code") != "fixture-code" || r.Form.Get("client_secret") != "fixture-secret" ||
			r.Form.Get("redirect_uri") != auth.Get("redirect_uri") ||
			base64.RawURLEncoding.EncodeToString(verifier[:]) != auth.Get("code_challenge") {
			t.Error("invalid token exchange")
		}
		fmt.Fprintf(w, `{"refresh_token":"fixture-refresh","scope":%q}`, gmailReadonly)
	}))
	defer tokenEndpoint.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := authorizeGmail(ctx, "https://example.invalid/auth", tokenEndpoint.URL, "fixture-client", "fixture-secret", func(address string) error {
		parsed, err := url.Parse(address)
		if err != nil {
			return err
		}
		auth = parsed.Query()
		if auth.Get("scope") != gmailReadonly || auth.Get("access_type") != "offline" ||
			auth.Get("code_challenge_method") != "S256" || auth.Get("state") == "" {
			t.Fatal("invalid authorization parameters")
		}
		client := &http.Client{Timeout: time.Second}
		callback := func(state string, status int) error {
			response, err := client.Get(auth.Get("redirect_uri") + "?" + url.Values{"state": {state}, "code": {"fixture-code"}}.Encode())
			if err != nil {
				return err
			}
			response.Body.Close()
			if response.StatusCode != status {
				t.Errorf("callback status = %d, want %d", response.StatusCode, status)
			}
			return nil
		}
		if err := callback("wrong-state", http.StatusBadRequest); err != nil {
			return err
		}
		return callback(auth.Get("state"), http.StatusOK)
	})
	if err != nil || got != "fixture-refresh" {
		t.Fatalf("authorization did not return the fixture refresh token: %v", err)
	}
}

func TestGmailLabels(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("missing access token")
		}
		fmt.Fprint(w, `{"labels":[{"id":"Label_1","name":"Newsletter","type":"user"}]}`)
	}))
	defer api.Close()
	labels, err := gmailLabels(context.Background(), api.URL, "fixture-token")
	if err != nil || len(labels) != 1 || labels[0].ID != "Label_1" {
		t.Fatalf("labels: %+v, %v", labels, err)
	}
}
