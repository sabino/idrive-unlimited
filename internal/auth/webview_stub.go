//go:build !cgo

package auth

import (
	"context"
	"fmt"

	"idrive-unlimited/internal/session"
)

func Login(context.Context) (*session.Data, error) {
	return nil, fmt.Errorf("native embedded login requires CGO-enabled builds; rebuild with CGO_ENABLED=1 on Windows or Linux to use the lightweight webview auth flow")
}
