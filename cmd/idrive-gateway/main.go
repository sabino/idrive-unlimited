package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"idrive-unlimited/internal/auth"
	"idrive-unlimited/internal/evs"
	"idrive-unlimited/internal/probe"
	"idrive-unlimited/internal/session"
	idrivewebdav "idrive-unlimited/internal/webdav"

	netwebdav "golang.org/x/net/webdav"
)

func main() {
	log.SetFlags(0)

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	ctx := context.Background()
	var err error

	switch os.Args[1] {
	case "login":
		err = runLogin(ctx, os.Args[2:])
	case "probe":
		err = runProbe(ctx, os.Args[2:])
	case "smoke":
		err = runSmoke(ctx, os.Args[2:])
	case "serve":
		err = runServe(ctx, os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `idrive-gateway bridges IDrive Cloud Backup to rclone over a local WebDAV server.

Usage:
  idrive-gateway login [--config PATH] [--force]
  idrive-gateway probe [--config PATH] [--root /] [--scratch /.idrive-gateway-probe] [--json]
  idrive-gateway smoke [--config PATH] [--root /] [--mount-drive X:]
  idrive-gateway serve [--config PATH] [--root /] [--listen 127.0.0.1:8787] [--dav-user idrive] [--dav-pass PASS]

Notes:
  - login opens a lightweight native webview instead of bundling Electron.
  - Windows uses WebView2. Linux uses the system WebKitGTK runtime used by webview_go.
  - serve listens on loopback only; use stock "rclone webdav" against it.
`)
}

func runLogin(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	storePath := fs.String("config", "", "session file path")
	force := fs.Bool("force", false, "ignore an existing valid session and open login again")
	if err := fs.Parse(args); err != nil {
		return err
	}

	store, err := session.NewStore(*storePath)
	if err != nil {
		return err
	}

	if !*force {
		if data, err := store.Load(); err == nil {
			client, err := evs.NewClientFromSession(data)
			if err == nil && client.Validate(ctx) == nil {
				fmt.Printf("Session already valid.\nServer: %s\nConfig: %s\n", data.ServerHost, store.Path)
				return nil
			}
		}
	}

	data, err := auth.Login(ctx)
	if err != nil {
		return err
	}
	if err := store.Save(data); err != nil {
		return err
	}

	fmt.Printf("Login succeeded.\nServer: %s\nConfig: %s\n", data.ServerHost, store.Path)
	fmt.Println("The saved session is an EVS cookie session; your password is not stored by this utility.")
	return nil
}

func runProbe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	storePath := fs.String("config", "", "session file path")
	rootPath := fs.String("root", "/", "mounted root path inside Cloud Backup")
	scratchPath := fs.String("scratch", "/.idrive-gateway-probe", "temporary remote path used for capability tests")
	jsonOut := fs.Bool("json", false, "print the full probe report as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client, _, err := ensureClient(ctx, *storePath, true)
	if err != nil {
		return err
	}

	report, err := probe.Run(ctx, client, *rootPath, *scratchPath)
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(report); encErr != nil {
			return encErr
		}
	} else {
		printProbeReport(report)
	}
	return err
}

func runServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	storePath := fs.String("config", "", "session file path")
	rootPath := fs.String("root", "/", "mounted root path inside Cloud Backup")
	listenAddr := fs.String("listen", "127.0.0.1:8787", "loopback listen address")
	davUser := fs.String("dav-user", "idrive", "basic auth username for the local WebDAV server")
	davPass := fs.String("dav-pass", "", "basic auth password for the local WebDAV server")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client, data, err := ensureClient(ctx, *storePath, true)
	if err != nil {
		return err
	}

	password := *davPass
	if password == "" {
		password, err = randomSecret(24)
		if err != nil {
			return err
		}
	}

	fileSystem := idrivewebdav.New(client, *rootPath)
	handler := &netwebdav.Handler{
		FileSystem: fileSystem,
		LockSystem: netwebdav.NewMemLS(),
		Logger: func(req *http.Request, err error) {
			if err != nil && !errors.Is(err, evs.ErrNotFound) {
				log.Printf("%s %s -> %v", req.Method, req.URL.Path, err)
			}
		},
	}

	server := &http.Server{
		Addr:              *listenAddr,
		Handler:           basicAuth(*davUser, password, handler),
		ReadHeaderTimeout: 15 * time.Second,
	}

	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-sigCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	fmt.Printf("IDrive server: %s\n", data.ServerHost)
	fmt.Printf("WebDAV root: %s\n", idrivewebdav.CleanPath(*rootPath))
	fmt.Printf("Listening on: http://%s/\n", *listenAddr)
	fmt.Printf("Username: %s\n", *davUser)
	fmt.Printf("Password: %s\n", password)
	fmt.Println()
	fmt.Println("rclone example:")
	fmt.Printf("  rclone config create idrive-local webdav url http://%s vendor other user %s pass <obscured-password>\n", *listenAddr, *davUser)
	fmt.Printf("  rclone ls idrive-local:\n")
	fmt.Printf("  rclone mount idrive-local: X: --vfs-cache-mode full\n")
	fmt.Println()
	fmt.Println("Press Ctrl+C to stop.")

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func ensureClient(ctx context.Context, storePath string, interactive bool) (*evs.Client, *session.Data, error) {
	store, err := session.NewStore(storePath)
	if err != nil {
		return nil, nil, err
	}

	if data, err := store.Load(); err == nil {
		client, err := evs.NewClientFromSession(data)
		if err == nil && client.Validate(ctx) == nil {
			return client, data, nil
		}
	}

	if !interactive {
		return nil, nil, fmt.Errorf("no valid session found; run `idrive-gateway login` first")
	}

	data, err := auth.Login(ctx)
	if err != nil {
		return nil, nil, err
	}
	if err := store.Save(data); err != nil {
		return nil, nil, err
	}
	client, err := evs.NewClientFromSession(data)
	if err != nil {
		return nil, nil, err
	}
	return client, data, nil
}

func basicAuth(user, pass string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, ok := r.BasicAuth()
		if !ok || gotUser != user || gotPass != pass {
			w.Header().Set("WWW-Authenticate", `Basic realm="idrive-gateway"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func randomSecret(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return strings.TrimRight(base64.RawURLEncoding.EncodeToString(buf), "="), nil
}

func printProbeReport(report *probe.Report) {
	fmt.Printf("Probe root: %s\n", report.RootPath)
	fmt.Printf("Scratch path: %s\n", report.ScratchPath)
	fmt.Printf("Started: %s\n", report.StartedAt.Format(time.RFC3339))
	fmt.Printf("Finished: %s\n", report.FinishedAt.Format(time.RFC3339))
	fmt.Println()
	for _, step := range report.Steps {
		status := "OK"
		if !step.OK {
			status = "FAIL"
		}
		if step.Detail != "" {
			fmt.Printf("[%s] %s: %s\n", status, step.Name, step.Detail)
			continue
		}
		fmt.Printf("[%s] %s\n", status, step.Name)
	}
	if report.Success {
		fmt.Println()
		fmt.Println("Probe completed successfully.")
		return
	}
	fmt.Println()
	fmt.Println("Probe failed.")
}
