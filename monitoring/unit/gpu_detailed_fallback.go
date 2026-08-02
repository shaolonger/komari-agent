//go:build !linux

package monitoring

func newPlatformGPUProviders() []gpuProvider {
	return nil
}
