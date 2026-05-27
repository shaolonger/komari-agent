//go:build !windows

package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAutoDiscoveryConfigTightensExistingPermissions(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "auto-discovery.json")
	useAutoDiscoveryConfigPath(t, configPath)

	if err := os.WriteFile(configPath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	if err := os.Chmod(configPath, 0o644); err != nil {
		t.Fatalf("os.Chmod() error = %v", err)
	}

	if err := saveAutoDiscoveryConfig(&AutoDiscoveryConfig{UUID: "test-uuid", Token: "test-token"}); err != nil {
		t.Fatalf("saveAutoDiscoveryConfig() error = %v", err)
	}

	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("os.Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != autoDiscoveryConfigFilePerm {
		t.Fatalf("file mode = %o, want %o", got, autoDiscoveryConfigFilePerm)
	}
}