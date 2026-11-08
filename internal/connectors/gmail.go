package connectors

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/sam-bretz/braindex/internal/ingest"
	"github.com/sam-bretz/braindex/internal/library"
)

// SyncGmail imports a single Gmail label using a read-only OAuth access token.
// Successful saves can be skipped on retries by supplying alreadyStored.
func SyncGmail(ctx context.Context, labelID, token string, alreadyStored func(string) bool, save func(library.Source) error) (int, error) {
	if !providerID.MatchString(labelID) || strings.TrimSpace(token) == "" || save == nil {
		return 0, fmt.Errorf("Gmail label ID, read-only access token, and save handler are required")
	}
	return syncGmail(ctx, "https://gmail.googleapis.com/gmail/v1/users/me/messages", labelID, token, alreadyStored, save)
}

func syncGmail(ctx context.Context, endpoint, labelID, token string, alreadyStored func(string) bool, save func(library.Source) error) (int, error) {
	headers := map[string]string{"Authorization": "Bearer " + token}
	query := url.Values{"labelIds": {labelID}, "maxResults": {"100"}}
	seenPages := map[string]bool{}
	seenMessages := map[string]bool{}
	var failures []error
	saved := 0
	for {
		pageToken := query.Get("pageToken")
		if seenPages[pageToken] {
			failures = append(failures, fmt.Errorf("Gmail returned a repeated page token"))
			break
		}
		seenPages[pageToken] = true
		data, _, err := get(ctx, endpoint+"?"+query.Encode(), headers, maxPublicResponse)
		if err != nil {
			failures = append(failures, err)
			break
		}
		var page struct {
			Next     string `json:"nextPageToken"`
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			failures = append(failures, fmt.Errorf("read Gmail message list: %w", err))
			break
		}
		for _, message := range page.Messages {
			if err := ctx.Err(); err != nil {
				return saved, errors.Join(append(failures, err)...)
			}
			if !providerID.MatchString(message.ID) || seenMessages[message.ID] {
				continue
			}
			seenMessages[message.ID] = true
			sourceID := ingest.StableID("gmail", message.ID)
			if alreadyStored != nil && alreadyStored(sourceID) {
				continue
			}
			source, present, err := fetchGmailMessage(ctx, endpoint, message.ID, labelID, headers)
			if err == nil && present {
				source.ID = sourceID
				err = save(source)
				if err == nil {
					saved++
				}
			}
			if err != nil {
				failures = append(failures, fmt.Errorf("Gmail message %s: %w", message.ID, err))
			}
		}
		if page.Next == "" {
			break
		}
		query.Set("pageToken", page.Next)
	}
	return saved, errors.Join(failures...)
}

func fetchGmailMessage(ctx context.Context, endpoint, id, labelID string, headers map[string]string) (library.Source, bool, error) {
	data, _, err := get(ctx, endpoint+"/"+id+"?format=raw", headers, 90<<20)
	if err != nil {
		return library.Source{}, false, err
	}
	var message struct {
		Raw    string   `json:"raw"`
		Labels []string `json:"labelIds"`
	}
	if err := json.Unmarshal(data, &message); err != nil {
		return library.Source{}, false, err
	}
	if !slices.Contains(message.Labels, labelID) {
		return library.Source{}, false, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(message.Raw, "="))
	if err != nil {
		return library.Source{}, false, err
	}
	source, err := ingest.ExtractEmail(raw, "")
	if source.Title == "" {
		source.Title = "Gmail message " + id
	}
	return source, err == nil, err
}

// RefreshGmailToken exchanges an existing OAuth refresh token for an access
// token so a scheduler can run label sync without interactive authorization.
func RefreshGmailToken(ctx context.Context, clientID, clientSecret, refreshToken string) (string, error) {
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(refreshToken) == "" {
		return "", fmt.Errorf("Gmail OAuth client ID and refresh token are required")
	}
	return refreshGmailToken(ctx, "https://oauth2.googleapis.com/token", clientID, clientSecret, refreshToken)
}

func refreshGmailToken(ctx context.Context, endpoint, clientID, clientSecret, refreshToken string) (string, error) {
	form := url.Values{
		"client_id": {clientID}, "refresh_token": {refreshToken}, "grant_type": {"refresh_token"},
	}
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}
	data, _, err := request(ctx, http.MethodPost, endpoint, map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	}, strings.NewReader(form.Encode()), 64<<10)
	if err != nil {
		return "", fmt.Errorf("refresh Gmail access token: %w", err)
	}
	var response struct {
		Token string `json:"access_token"`
		Type  string `json:"token_type"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return "", fmt.Errorf("invalid Gmail OAuth token response")
	}
	if response.Token == "" || !strings.EqualFold(response.Type, "Bearer") {
		return "", fmt.Errorf("Gmail OAuth did not return a bearer token")
	}
	return response.Token, nil
}
