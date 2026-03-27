package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	idrivewebdav "idrive-unlimited/internal/webdav"

	netwebdav "golang.org/x/net/webdav"
)

func runSmoke(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("smoke", flag.ContinueOnError)
	storePath := fs.String("config", "", "session file path")
	rootPath := fs.String("root", "/", "mounted root path inside Cloud Backup")
	mountDrive := fs.String("mount-drive", "", "optional Windows drive letter for a temporary rclone mount, for example X:")
	browsePath := fs.String("browse-path", "", "optional mounted path to time after mount, for example /Phone Backup/Photos")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if _, err := exec.LookPath("rclone"); err != nil {
		return fmt.Errorf("rclone not found in PATH: %w", err)
	}

	client, _, err := ensureClient(ctx, *storePath, true)
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()

	server := &http.Server{
		Handler: basicAuth("idrive", "localtest123", &netwebdav.Handler{
			FileSystem: idrivewebdav.New(client, *rootPath),
			LockSystem: netwebdav.NewMemLS(),
		}),
		ReadHeaderTimeout: 15 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Serve(listener)
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		select {
		case <-serverErr:
		default:
		}
	}()

	fmt.Printf("Smoke WebDAV endpoint: http://%s/\n", listener.Addr().String())

	tempDir, err := os.MkdirTemp("", "idrive-gateway-smoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	configPath := filepath.Join(tempDir, "rclone.conf")
	localPath := filepath.Join(tempDir, "smoke.txt")
	if err := os.WriteFile(localPath, []byte("hello from rclone smoke\n"), 0o600); err != nil {
		return err
	}

	obscuredPass, err := runCommandCapture(ctx, "", "rclone", "obscure", "localtest123")
	if err != nil {
		return err
	}
	obscuredPass = strings.TrimSpace(obscuredPass)

	if _, err := runCommandCapture(ctx, "", "rclone",
		"--config", configPath,
		"config", "create",
		"idrive-smoke",
		"webdav",
		"url", "http://"+listener.Addr().String(),
		"vendor", "other",
		"user", "idrive",
		"pass", obscuredPass,
	); err != nil {
		return err
	}

	if out, err := runCommandCapture(ctx, "", "rclone", "--config", configPath, "lsd", "idrive-smoke:"); err != nil {
		return err
	} else {
		fmt.Println("rclone lsd succeeded.")
		trimmed := strings.TrimSpace(out)
		if trimmed != "" {
			fmt.Println(trimmed)
		}
	}

	if browseTarget := strings.TrimSpace(*browsePath); browseTarget != "" {
		listStart := time.Now()
		out, err := runCommandCapture(ctx, "", "rclone", "--config", configPath, "lsf", "idrive-smoke:"+browseTarget)
		listElapsed := time.Since(listStart)
		if err != nil {
			return fmt.Errorf("rclone lsf %s failed: %w", browseTarget, err)
		}
		lines := 0
		trimmed := strings.TrimSpace(out)
		if trimmed != "" {
			lines = len(strings.Split(trimmed, "\n"))
		}
		fmt.Printf("rclone lsf %s succeeded in %s with %d entries.\n", browseTarget, listElapsed.Round(time.Millisecond), lines)
	}

	remoteDir := "/rclone-smoke-" + time.Now().UTC().Format("20060102t150405")
	remoteFile := "idrive-smoke:" + remoteDir + "/smoke.txt"
	remoteRenamed := "idrive-smoke:" + remoteDir + "/smoke-renamed.txt"

	steps := []struct {
		label string
		args  []string
	}{
		{label: "mkdir", args: []string{"--config", configPath, "mkdir", "idrive-smoke:" + remoteDir}},
		{label: "copyto", args: []string{"--config", configPath, "copyto", localPath, remoteFile}},
	}
	for _, step := range steps {
		if _, err := runCommandCapture(ctx, "", "rclone", step.args...); err != nil {
			return fmt.Errorf("%s failed: %w", step.label, err)
		}
		fmt.Printf("rclone %s succeeded.\n", step.label)
	}

	catOut, err := runCommandCapture(ctx, "", "rclone", "--config", configPath, "cat", remoteFile)
	if err != nil {
		return err
	}
	if strings.TrimSpace(catOut) != "hello from rclone smoke" {
		return fmt.Errorf("rclone cat returned unexpected content: %q", strings.TrimSpace(catOut))
	}
	fmt.Println("rclone cat succeeded.")

	postSteps := []struct {
		label string
		args  []string
	}{
		{label: "moveto", args: []string{"--config", configPath, "moveto", remoteFile, remoteRenamed}},
		{label: "deletefile", args: []string{"--config", configPath, "deletefile", remoteRenamed}},
		{label: "purge", args: []string{"--config", configPath, "purge", "idrive-smoke:" + remoteDir}},
	}
	for _, step := range postSteps {
		if _, err := runCommandCapture(ctx, "", "rclone", step.args...); err != nil {
			return fmt.Errorf("%s failed: %w", step.label, err)
		}
		fmt.Printf("rclone %s succeeded.\n", step.label)
	}

	if drive := strings.TrimSpace(*mountDrive); drive != "" {
		if err := runMountSmoke(ctx, configPath, drive, *browsePath); err != nil {
			return err
		}
	}

	fmt.Println("rclone smoke test passed.")
	return nil
}

func runMountSmoke(ctx context.Context, configPath, drive, browsePath string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("--mount-drive is only supported on Windows")
	}

	drive = strings.ToUpper(strings.TrimSpace(drive))
	drive = strings.TrimSuffix(drive, ":")
	if len(drive) != 1 || drive[0] < 'A' || drive[0] > 'Z' {
		return fmt.Errorf("invalid drive letter %q", drive)
	}

	mountPath := drive + ":"
	if _, err := os.Stat(mountPath + `\`); err == nil {
		return fmt.Errorf("mount drive %s is already in use", mountPath)
	}

	mountCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(mountCtx, "rclone",
		"--config", configPath,
		"mount", "idrive-smoke:",
		mountPath,
		"--network-mode",
		"--vfs-cache-mode", "full",
	)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start rclone mount: %w", err)
	}
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	start := time.Now()
	for time.Since(start) < 20*time.Second {
		if _, statErr := os.Stat(mountPath + `\`); statErr == nil {
			fmt.Printf("rclone mount succeeded on %s.\n", mountPath)
			if browseTarget := strings.TrimSpace(browsePath); browseTarget != "" {
				target := filepath.Join(mountPath+`\`, filepath.FromSlash(strings.TrimPrefix(browseTarget, "/")))
				browseStart := time.Now()
				type browseResult struct {
					entries int
					err     error
				}
				resultCh := make(chan browseResult, 1)
				go func() {
					entries, err := os.ReadDir(target)
					resultCh <- browseResult{entries: len(entries), err: err}
				}()
				select {
				case result := <-resultCh:
					if result.err != nil {
						cancel()
						<-waitCh
						return fmt.Errorf("browse mounted path %s: %w", browseTarget, result.err)
					}
					fmt.Printf("mounted browse %s succeeded in %s with %d entries.\n", browseTarget, time.Since(browseStart).Round(time.Millisecond), result.entries)
				case <-time.After(60 * time.Second):
					cancel()
					<-waitCh
					return fmt.Errorf("mounted browse %s timed out after 60s", browseTarget)
				}
			}
			cancel()
			<-waitCh
			return nil
		}
		select {
		case err := <-waitCh:
			text := strings.TrimSpace(output.String())
			if text == "" {
				return fmt.Errorf("rclone mount exited before the drive appeared: %w", err)
			}
			return fmt.Errorf("rclone mount exited before the drive appeared: %w: %s", err, text)
		default:
		}
		time.Sleep(1 * time.Second)
	}

	cancel()
	<-waitCh
	return fmt.Errorf("mount drive %s did not appear within 20s: %s", mountPath, strings.TrimSpace(output.String()))
}

func runCommandCapture(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(output))
		if text == "" {
			return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, text)
	}
	return string(output), nil
}
