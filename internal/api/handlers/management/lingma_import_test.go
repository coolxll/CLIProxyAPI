package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/lingma"
)

func TestBuildLingmaCredentialMetadataIncludesProviderType(t *testing.T) {
	creds := &lingma.Credentials{
		MachineID:          "machine-1",
		UID:                "user-1",
		OrganizationID:     "org-1",
		CosyKey:            "cosy-key",
		SecurityOAuthToken: "security-token",
		EncryptUserInfo:    "encrypted-user",
		UserType:           "personal",
		ExpireTime:         1893456000,
	}

	meta := buildLingmaCredentialMetadata("lingma001", creds)
	if got, _ := meta["type"].(string); got != "lingma" {
		t.Fatalf("type = %q, want lingma", got)
	}
	if got, _ := meta["name"].(string); got != "lingma001" {
		t.Fatalf("name = %q, want lingma001", got)
	}
	if got, _ := meta["uid"].(string); got != "user-1" {
		t.Fatalf("uid = %q, want user-1", got)
	}
	if _, ok := meta["expires_at"]; !ok {
		t.Fatal("expected expires_at to be set")
	}
}

func TestLingmaCredentialFileNameSanitizesComponents(t *testing.T) {
	got := lingmaCredentialFileName("../bad name", "user/1")
	if got != "lingma-bad-name-user-1.json" {
		t.Fatalf("file name = %q, want lingma-bad-name-user-1.json", got)
	}
}
