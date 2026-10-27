//go:build !windows && cgo

package auth

func ensureNativeLoginRuntime() error {
	return nil
}
