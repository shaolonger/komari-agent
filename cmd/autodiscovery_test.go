package cmd

import (
	"path/filepath"
	"testing"
)

func useAutoDiscoveryConfigPath(t *testing.T, path string) {
	t.Helper()

	previous := autoDiscoveryConfigPath
	autoDiscoveryConfigPath = func() string {
		return path
	}

	t.Cleanup(func() {
		autoDiscoveryConfigPath = previous
	})
}

func TestSaveAndLoadAutoDiscoveryConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "auto-discovery.json")
	useAutoDiscoveryConfigPath(t, configPath)

	want := &AutoDiscoveryConfig{
		UUID:  "test-uuid",
		Token: "test-token",
	}

	if err := saveAutoDiscoveryConfig(want); err != nil {
		t.Fatalf("saveAutoDiscoveryConfig() error = %v", err)
	}

	got, err := loadAutoDiscoveryConfig()
	if err != nil {
		t.Fatalf("loadAutoDiscoveryConfig() error = %v", err)
	}
	if got == nil {
		t.Fatal("loadAutoDiscoveryConfig() returned nil config")
	}
	if *got != *want {
		t.Fatalf("loadAutoDiscoveryConfig() = %#v, want %#v", *got, *want)
	}
}