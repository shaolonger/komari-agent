//go:build windows

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func useAutoDiscoveryExpectedOwnerSIDs(t *testing.T, ownerSIDs []string) {
	t.Helper()

	previous := autoDiscoveryExpectedOwnerSIDs
	autoDiscoveryExpectedOwnerSIDs = func() ([]string, error) {
		return ownerSIDs, nil
	}

	t.Cleanup(func() {
		autoDiscoveryExpectedOwnerSIDs = previous
	})
}

func applyWindowsTestDACL(path string, sddl string) error {
	securityDescriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("build security descriptor: %w", err)
	}
	acl, _, err := securityDescriptor.DACL()
	if err != nil {
		return fmt.Errorf("extract dacl: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		return fmt.Errorf("apply protected dacl: %w", err)
	}

	return nil
}

func TestSaveAutoDiscoveryConfigRestrictsWindowsACL(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "config", autoDiscoveryConfigDirName, autoDiscoveryConfigFileName)
	legacyPath := filepath.Join(baseDir, "legacy", autoDiscoveryConfigFileName)
	useAutoDiscoveryPaths(t, configPath, legacyPath)

	if err := saveAutoDiscoveryConfig(&AutoDiscoveryConfig{UUID: "test-uuid", Token: "test-token"}); err != nil {
		t.Fatalf("saveAutoDiscoveryConfig() error = %v", err)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("os.Stat() error = %v", err)
	}

	sd, err := windows.GetNamedSecurityInfo(configPath, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("windows.GetNamedSecurityInfo() error = %v", err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatalf("sd.Owner() error = %v", err)
	}
	if owner == nil || !owner.IsValid() {
		t.Fatal("sd.Owner() returned invalid sid")
	}

	sddl := sd.String()
	if !strings.Contains(sddl, "D:P") {
		t.Fatalf("security descriptor is not protected: %s", sddl)
	}
	for _, trustee := range expectedWindowsAutoDiscoveryTrustees(owner) {
		if !strings.Contains(sddl, trustee) {
			t.Fatalf("security descriptor %q does not include trustee %q", sddl, trustee)
		}
	}
}

func TestLoadAutoDiscoveryConfigRejectsPermissiveACL(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "config", autoDiscoveryConfigDirName, autoDiscoveryConfigFileName)
	legacyPath := filepath.Join(baseDir, "legacy", autoDiscoveryConfigFileName)
	useAutoDiscoveryPaths(t, configPath, legacyPath)

	if err := saveAutoDiscoveryConfig(&AutoDiscoveryConfig{UUID: "test-uuid", Token: "test-token"}); err != nil {
		t.Fatalf("saveAutoDiscoveryConfig() error = %v", err)
	}

	sd, err := windows.GetNamedSecurityInfo(configPath, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("windows.GetNamedSecurityInfo() error = %v", err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatalf("sd.Owner() error = %v", err)
	}
	if owner == nil || !owner.IsValid() {
		t.Fatal("sd.Owner() returned invalid sid")
	}

	insecureSDDL := buildAutoDiscoveryFileSDDL(owner.String()) + "(A;;GR;;;WD)"
	if err := applyWindowsTestDACL(configPath, insecureSDDL); err != nil {
		t.Fatalf("applyWindowsTestDACL() error = %v", err)
	}

	_, err = loadAutoDiscoveryConfig()
	if err == nil {
		t.Fatal("loadAutoDiscoveryConfig() error = nil, want permission error")
	}
	if !strings.Contains(err.Error(), "unexpected ace count") {
		t.Fatalf("loadAutoDiscoveryConfig() error = %v, want ACL validation failure", err)
	}
}

func TestLoadAutoDiscoveryConfigRejectsUnexpectedOwner(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "config", autoDiscoveryConfigDirName, autoDiscoveryConfigFileName)
	legacyPath := filepath.Join(baseDir, "legacy", autoDiscoveryConfigFileName)
	useAutoDiscoveryPaths(t, configPath, legacyPath)

	if err := saveAutoDiscoveryConfig(&AutoDiscoveryConfig{UUID: "test-uuid", Token: "test-token"}); err != nil {
		t.Fatalf("saveAutoDiscoveryConfig() error = %v", err)
	}

	owner, err := autoDiscoveryPathOwnerSID(configPath)
	if err != nil {
		t.Fatalf("autoDiscoveryPathOwnerSID() error = %v", err)
	}
	useAutoDiscoveryExpectedOwnerSIDs(t, []string{owner.String() + "-mismatch"})

	_, err = loadAutoDiscoveryConfig()
	if err == nil {
		t.Fatal("loadAutoDiscoveryConfig() error = nil, want owner validation failure")
	}
	if !strings.Contains(err.Error(), "does not match expected sids") {
		t.Fatalf("loadAutoDiscoveryConfig() error = %v, want owner validation failure", err)
	}
}
