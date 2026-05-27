//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
)

func enforceAutoDiscoveryConfigPermissions(path string) error {
	return os.Chmod(path, autoDiscoveryConfigFilePerm)
}

func validateAutoDiscoveryConfigPermissions(path string) error {
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
