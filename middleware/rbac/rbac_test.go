package rbac

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateFileContent(t *testing.T) {
	policyPath := filepath.Join(t.TempDir(), "policies.csv")
	if err := os.WriteFile(policyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateFileContent(policyPath); err == nil {
		t.Fatal("expected empty policy file to be rejected")
	}

	if err := os.WriteFile(policyPath, []byte("p, admin, /api, GET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateFileContent(policyPath); err != nil {
		t.Fatalf("expected non-empty policy file to be accepted: %v", err)
	}
}
