package rbac

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestInitializeEnforcerRetainsLastKnownGoodOnInvalidPolicy(t *testing.T) {
	tempDir := t.TempDir()
	modelPath := filepath.Join(tempDir, "model.conf")
	policyPath := filepath.Join(tempDir, "policies.csv")
	model := `[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = r.sub == p.sub && keyMatch(r.obj, p.obj) && regexMatch(r.act, p.act)
`
	require.NoError(t, os.WriteFile(modelPath, []byte(model), 0o600))
	require.NoError(t, os.WriteFile(policyPath, []byte("p, admin, /old, GET\n"), 0o600))

	previousModelPath := localModelFile
	previousPolicyPath := localPolicyFile
	enforcerMutex.Lock()
	previousEnforcer := syncedCachedEnforcer
	enforcerMutex.Unlock()
	t.Cleanup(func() {
		localModelFile = previousModelPath
		localPolicyFile = previousPolicyPath
		enforcerMutex.Lock()
		syncedCachedEnforcer = previousEnforcer
		enforcerMutex.Unlock()
	})
	localModelFile = modelPath
	localPolicyFile = policyPath

	require.NoError(t, initializeEnforcer())
	enforcerMutex.RLock()
	lastKnownGood := syncedCachedEnforcer
	allowed, err := syncedCachedEnforcer.Enforce("admin", "/old", "GET")
	enforcerMutex.RUnlock()
	require.NoError(t, err)
	assert.True(t, allowed)

	require.NoError(t, os.WriteFile(policyPath, []byte("p, \"unterminated\n"), 0o600))
	require.Error(t, reloadPolicy())

	enforcerMutex.RLock()
	current := syncedCachedEnforcer
	allowed, err = syncedCachedEnforcer.Enforce("admin", "/old", "GET")
	enforcerMutex.RUnlock()
	require.NoError(t, err)
	assert.Same(t, lastKnownGood, current)
	assert.True(t, allowed)
}
