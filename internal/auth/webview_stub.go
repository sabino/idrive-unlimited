//go:build !cgo

package auth

import (
	"context"
	"fmt"

	"idrive-unlimited/internal/session"
)

func Login(context.Context) (*session.Data, error) {
	return nil, fmt.Errorf("native embedded login requires a CGO-enabled build; rebuild with CGO_ENABLED=1 on Windows, macOS, or Linux to use the lightweight webview auth flow")
}
