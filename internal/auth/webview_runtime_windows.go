//go:build windows

package auth

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const webView2ClientGUID = "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"

func ensureNativeLoginRuntime() error {
	if hasWebView2Runtime() {
		return nil
	}
	return fmt.Errorf("Microsoft Edge WebView2 Runtime is required for `idrive-gateway login` on Windows. Install it with `winget install --id Microsoft.EdgeWebView2Runtime` or use the Windows native installer/native zip, which includes the Evergreen bootstrapper")
}

func hasWebView2Runtime() bool {
	paths := []struct {
		root registry.Key
		path string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + webView2ClientGUID},
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\` + webView2ClientGUID},
		{registry.CURRENT_USER, `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + webView2ClientGUID},
	}

	for _, candidate := range paths {
		key, err := registry.OpenKey(candidate.root, candidate.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}

		for _, valueName := range []string{"pv", "name"} {
			value, _, err := key.GetStringValue(valueName)
			if err == nil && strings.TrimSpace(value) != "" {
				key.Close()
				return true
			}
		}

		key.Close()
	}

	return false
}
