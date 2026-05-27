//go:build windows

package cmd

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

const (
	autoDiscoveryAdministratorsSID = "BA"
	autoDiscoveryLocalSystemSID    = "SY"
)

func buildAutoDiscoveryFileSDDL(ownerSID string) string {
	trustees := []string{ownerSID, autoDiscoveryAdministratorsSID, autoDiscoveryLocalSystemSID}
	seen := make(map[string]struct{}, len(trustees))

	var builder strings.Builder
	builder.WriteString("D:P")
	for _, trustee := range trustees {
		if trustee == "" {
			continue
		}
		if _, ok := seen[trustee]; ok {
			continue
		}
		seen[trustee] = struct{}{}
		builder.WriteString("(A;;FA;;;")
		builder.WriteString(trustee)
		builder.WriteString(")")
	}

	return builder.String()
}

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

func validateAutoDiscoveryConfigPermissions(path string) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("get file security info: %w", err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("get file owner sid: %w", err)
	}
	if owner == nil || !owner.IsValid() {
		return fmt.Errorf("get file owner sid: invalid owner sid")
	}

	sddl := sd.String()
	daclIndex := strings.Index(sddl, "D:")
	if daclIndex == -1 {
		return fmt.Errorf("missing dacl in security descriptor %q", sddl)
	}
	dacl := sddl[daclIndex:]
	if !strings.HasPrefix(dacl, "D:P") {
		return fmt.Errorf("dacl is not protected: %q", dacl)
	}

	trustees := expectedWindowsAutoDiscoveryTrustees(owner)
	if aceCount := strings.Count(dacl, "("); aceCount != len(trustees) {
		return fmt.Errorf("unexpected ace count %d in dacl %q", aceCount, dacl)
	}
	for _, trustee := range trustees {
		if !strings.Contains(dacl, trustee) {
			return fmt.Errorf("missing trustee %q in dacl %q", trustee, dacl)
		}
	}

	return nil
}

func enforceAutoDiscoveryConfigPermissions(path string) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("get file owner: %w", err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("get file owner sid: %w", err)
	}
	if owner == nil || !owner.IsValid() {
		return fmt.Errorf("get file owner sid: invalid owner sid")
	}

	securityDescriptor, err := windows.SecurityDescriptorFromString(buildAutoDiscoveryFileSDDL(owner.String()))
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
