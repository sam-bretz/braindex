package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sam-bretz/braindex/internal/connectors"
)

type gmailCredential struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
	Refresh      string `json:"refresh_token"`
}

func (c command) gmail(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: braindex gmail login --client-file PATH | labels")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	switch args[0] {
	case "login":
		flags := newFlags("gmail login")
		clientFile := flags.String("client-file", "", "Google Desktop OAuth client JSON")
		pos, err := parse(flags, args[1:])
		if err != nil {
			return err
		}
		if len(pos) != 0 || *clientFile == "" {
			return errors.New("usage: braindex gmail login --client-file PATH")
		}
		data, err := os.ReadFile(*clientFile)
		if err != nil {
			return fmt.Errorf("read Google Desktop OAuth client file: %w", err)
		}
		var config struct {
			Installed gmailCredential `json:"installed"`
		}
		if err := json.Unmarshal(data, &config); err != nil || config.Installed.ClientID == "" {
			return errors.New("expected a Google Desktop OAuth client JSON file")
		}
		path, err := gmailCredentialPath()
		if err != nil {
			return err
		}
		credential := config.Installed
		credential.Refresh, err = connectors.AuthorizeGmail(ctx, credential.ClientID, credential.ClientSecret, func(address string) error {
			_, err := fmt.Fprintln(c.out, "Open this Google consent URL in your browser:\n"+address)
			return err
		})
		if err != nil {
			return err
		}
		if err := saveGmailCredential(path, credential); err != nil {
			return err
		}
		_, err = fmt.Fprintln(c.out, "Gmail authorization saved. Run braindex gmail labels to find the label ID.")
		return err
	case "labels":
		if len(args) != 1 {
			return errors.New("usage: braindex gmail labels")
		}
		token, err := gmailToken(ctx)
		if err != nil {
			return err
		}
		labels, err := connectors.GmailLabels(ctx, token)
		if err != nil {
			return err
		}
		return c.json(labels)
	default:
		return fmt.Errorf("unknown Gmail command %q", args[0])
	}
}

func gmailToken(ctx context.Context) (string, error) {
	if token := os.Getenv("BRAINDEX_GMAIL_TOKEN"); token != "" {
		return token, nil
	}
	if refresh := os.Getenv("BRAINDEX_GMAIL_REFRESH_TOKEN"); refresh != "" {
		return connectors.RefreshGmailToken(ctx, os.Getenv("BRAINDEX_GMAIL_CLIENT_ID"), os.Getenv("BRAINDEX_GMAIL_CLIENT_SECRET"), refresh)
	}
	path, err := gmailCredentialPath()
	if err != nil {
		return "", err
	}
	credential, err := loadGmailCredential(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", errors.New("Gmail is not connected; run braindex gmail login --client-file PATH")
	}
	if err != nil {
		return "", err
	}
	return connectors.RefreshGmailToken(ctx, credential.ClientID, credential.ClientSecret, credential.Refresh)
}

func gmailCredentialPath() (string, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(config, "braindex", "gmail.json"), nil
}

func loadGmailCredential(path string) (gmailCredential, error) {
	var credential gmailCredential
	info, err := os.Lstat(path)
	if err != nil {
		return credential, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return credential, errors.New("Gmail credential file must be a private regular file (mode 0600)")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return credential, err
	}
	if err := json.Unmarshal(data, &credential); err != nil || credential.ClientID == "" || credential.Refresh == "" {
		return gmailCredential{}, errors.New("saved Gmail credentials are invalid; run Gmail login again")
	}
	return credential, nil
}

func saveGmailCredential(path string, credential gmailCredential) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".gmail-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	writeErr := json.NewEncoder(f).Encode(credential)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
