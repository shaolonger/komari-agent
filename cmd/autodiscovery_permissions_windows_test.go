//go:build windows

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func expectedWindowsAutoDiscoveryTrustees(owner *windows.SID) []string {
	trustees := []string{autoDiscoveryAdministratorsSID, autoDiscoveryLocalSystemSID}
	if owner == nil {
		return trustees
	}
	if owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) || owner.IsWellKnown(windows.WinLocalSystemSid) {
		return trustees
	}
	return append(trustees, owner.String())
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
