//go:build !windows

package auth

func ensureNativeLoginRuntime() error {
	return nil
}
