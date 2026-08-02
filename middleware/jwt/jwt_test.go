package jwt

import (
	"path/filepath"
	"testing"

	"github.com/go-kratos/gateway/constants"
)

func TestGetPublicKeyPathUsesConfiguredPath(t *testing.T) {
	configuredPath := filepath.Join(t.TempDir(), "keys", "..", "public.pem")
	t.Setenv(constants.JwtPubkeyPath, configuredPath)

	if got, want := getPublicKeyPath(), filepath.Clean(configuredPath); got != want {
		t.Fatalf("getPublicKeyPath() = %q, want %q", got, want)
	}
}
