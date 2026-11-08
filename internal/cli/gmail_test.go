package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGmailCredentialPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "gmail.json")
	credential := gmailCredential{ClientID: "fixture-client", ClientSecret: "fixture-secret", Refresh: "fixture-refresh"}
	if err := saveGmailCredential(path, credential); err != nil {
		t.Fatal(err)
	}
	if saved, err := loadGmailCredential(path); err != nil || saved != credential {
		t.Fatalf("failed to read saved credential: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadGmailCredential(path); err == nil {
		t.Fatal("accepted a credential file readable by other users")
	}
}
