package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func useAutoDiscoveryPaths(t *testing.T, configPath string, legacyPath string) {
	t.Helper()

	previousConfigPath := autoDiscoveryConfigPath
	previousLegacyPath := autoDiscoveryLegacyConfigPath
	autoDiscoveryConfigPath = func() string {
		return configPath
	}
	autoDiscoveryLegacyConfigPath = func() string {
		return legacyPath
	}

	t.Cleanup(func() {
		autoDiscoveryConfigPath = previousConfigPath
		autoDiscoveryLegacyConfigPath = previousLegacyPath
	})
}

func TestSaveAndLoadAutoDiscoveryConfig(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "config", autoDiscoveryConfigDirName, autoDiscoveryConfigFileName)
	legacyPath := filepath.Join(baseDir, "legacy", autoDiscoveryConfigFileName)
	useAutoDiscoveryPaths(t, configPath, legacyPath)

	want := &AutoDiscoveryConfig{
		UUID:  "test-uuid",
		Token: "test-token",
	}

	if err := saveAutoDiscoveryConfig(want); err != nil {
		t.Fatalf("saveAutoDiscoveryConfig() error = %v", err)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("os.Stat() error = %v", err)
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

func TestLoadAutoDiscoveryConfigMigratesLegacyLocation(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "config", autoDiscoveryConfigDirName, autoDiscoveryConfigFileName)
	legacyPath := filepath.Join(baseDir, "legacy", autoDiscoveryConfigFileName)
	useAutoDiscoveryPaths(t, configPath, legacyPath)

	want := &AutoDiscoveryConfig{
		UUID:  "legacy-uuid",
		Token: "legacy-token",
	}
	legacyData, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(legacyPath, legacyData, autoDiscoveryConfigFilePerm); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
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
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy config still exists or stat failed: %v", err)
	}
	migrated, err := readAutoDiscoveryConfig(configPath)
	if err != nil {
		t.Fatalf("readAutoDiscoveryConfig() error = %v", err)
	}
	if *migrated != *want {
		t.Fatalf("migrated config = %#v, want %#v", *migrated, *want)
	}
}
