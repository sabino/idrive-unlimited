package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

func localPassword(password, filename string) (string, error) {
	if filename == "" {
		return password, nil
	}
	if password != "" {
		return "", fmt.Errorf("use only one of --dav-pass and --dav-pass-file")
	}
	f, err := os.Open(filename)
	if err != nil {
		return "", fmt.Errorf("open WebDAV password file: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return "", fmt.Errorf("WebDAV password file must be a regular private file (0600 on Unix)")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return "", err
	}
	password = strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if len(data) > 4096 || password == "" || strings.ContainsAny(password, "\r\n") {
		return "", fmt.Errorf("WebDAV password file must contain one nonempty line of at most 4096 bytes")
	}
	return password, nil
}
