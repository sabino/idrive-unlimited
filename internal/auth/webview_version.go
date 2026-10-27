package auth

import (
	"strconv"
	"strings"
)

func validWebViewVersion(value string) bool {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) != 4 {
		return false
	}
	nonzero := false
	for _, part := range parts {
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return false
		}
		nonzero = nonzero || n != 0
	}
	return nonzero
}
