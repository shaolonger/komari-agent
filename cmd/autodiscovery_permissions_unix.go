//go:build !windows

package cmd

import "os"

func enforceAutoDiscoveryConfigPermissions(path string) error {
	return os.Chmod(path, autoDiscoveryConfigFilePerm)
}