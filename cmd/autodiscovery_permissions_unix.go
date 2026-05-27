//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

var autoDiscoveryExpectedOwnerUID = func() (uint32, error) {
	return uint32(os.Geteuid()), nil
}

func autoDiscoveryPathOwnerUID(path string) (uint32, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("unsupported stat type %T", info.Sys())
	}
	return stat.Uid, nil
}

func enforceAutoDiscoveryConfigPermissions(path string) error {
	return os.Chmod(path, autoDiscoveryConfigFilePerm)
}

func validateAutoDiscoveryConfigPermissions(path string) error {
	expectedOwnerUID, err := autoDiscoveryExpectedOwnerUID()
	if err != nil {
		return fmt.Errorf("get expected owner uid: %w", err)
	}
	ownerUID, err := autoDiscoveryPathOwnerUID(path)
	if err != nil {
		return err
	}
	if ownerUID != expectedOwnerUID {
		return fmt.Errorf("file owner uid %d does not match expected uid %d", ownerUID, expectedOwnerUID)
	}
	dirOwnerUID, err := autoDiscoveryPathOwnerUID(filepath.Dir(path))
	if err != nil {
		return err
	}
	if dirOwnerUID != expectedOwnerUID {
		return fmt.Errorf("config directory owner uid %d does not match expected uid %d", dirOwnerUID, expectedOwnerUID)
	}

	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf("file mode %o is too permissive", mode)
	}

	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return err
	}
	if mode := dirInfo.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf("config directory mode %o is too permissive", mode)
	}

	return nil
}
