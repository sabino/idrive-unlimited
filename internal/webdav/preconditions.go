package idrivewebdav

import (
	"errors"
	"idrive-unlimited/internal/evs"
	"net/http"
	"strings"
)

// Evaluate write preconditions while the request holds the mutation guard. Weak
// metadata ETags must never satisfy If-Match's strong comparison requirement.
func checkPreconditions(w http.ResponseWriter, r *http.Request, fs *FileSystem) bool {
	match, none, since := r.Header.Get("If-Match"), r.Header.Get("If-None-Match"), r.Header.Get("If-Unmodified-Since")
	if match == "" && none == "" && since == "" {
		return true
	}
	node, err := fs.client.StatFresh(r.Context(), fs.remotePath(r.Context(), r.URL.Path))
	exists := err == nil
	if err != nil && !errors.Is(err, evs.ErrNotFound) {
		http.Error(w, "cannot validate write precondition", http.StatusServiceUnavailable)
		return false
	}
	tag := ""
	if exists {
		tag, _ = fs.info(node, r.URL.Path).ETag(r.Context())
	}
	failed := false
	if match != "" {
		failed = !exists || strings.TrimSpace(match) != "*"
	} else if since != "" && exists {
		if date, parseErr := http.ParseTime(since); parseErr == nil {
			failed = node.ModTime.IsZero() || node.ModTime.Unix() > date.Unix()
		}
	}
	if none != "" && exists {
		if strings.TrimSpace(none) == "*" {
			failed = true
		} else {
			for _, candidate := range strings.Split(none, ",") {
				if strings.TrimPrefix(strings.TrimSpace(candidate), "W/") == strings.TrimPrefix(tag, "W/") {
					failed = true
					break
				}
			}
		}
	}
	if failed {
		http.Error(w, "write precondition failed", http.StatusPreconditionFailed)
		return false
	}
	return true
}
