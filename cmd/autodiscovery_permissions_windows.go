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
