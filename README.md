# idrive-gateway

`idrive-gateway` exposes IDrive Cloud Backup as a local WebDAV endpoint so `rclone` can use it with the stock `webdav` backend.

It is rooted at Cloud Backup `/`, not at a single device folder. Top-level files, normal folders, and device-backed folders are all visible from the mount root.

## Why this shape

IDrive already has a working browser client and EVS API surface, but not a public rclone backend. The gateway keeps the compatibility burden small by:

- using the same EVS endpoints the web app uses for browse, upload, rename, move, copy, delete, and download
- keeping the runtime surface local-only through WebDAV
- letting `rclone` handle listing, copying, and mounting with its mature `webdav` integration

## Login model

The intended login flow is a lightweight native embedded browser via `webview_go`:

- Windows: WebView2
- macOS: WebKit
- Linux: WebKitGTK

That avoids Electron and avoids storing the user password in this utility.

When the embedded login reaches the authenticated IDrive home page, the gateway calls `/idrive/home/getTokenLogin`, exchanges that token for its own EVS session, and then uses that EVS session for all API traffic.

## Build

The core EVS client and WebDAV bridge build normally:

```powershell
go build ./...
```

The native embedded login requires `cgo` plus a working GCC-compatible C/C++ toolchain:

```powershell
$env:CGO_ENABLED='1'
go build ./...
```

If you build without `cgo`, `login` returns a clear error, but `probe` and `serve` still work when a valid EVS session file already exists.

Preferred Windows build command:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\build-windows.ps1 -VerboseEnv
```

That script:

- finds the MSYS2 GCC toolchain
- sets `CC`, `CXX`, and `CGO_ENABLED`
- builds `idrive-gateway.exe`
- copies the required MinGW runtime DLLs next to the exe

## Windows prerequisites

On Windows there are two separate dependency buckets:

- Build-time: a GCC-compatible C/C++ toolchain for `cgo`
- Runtime: the Microsoft Edge WebView2 runtime for the embedded login window

The compiler is not a runtime dependency. You install it to build the binary, not to run the finished binary.

Recommended build setup on Windows:

- Install a GCC-compatible Windows toolchain such as MSYS2 MinGW-w64/UCRT64
- Make sure `gcc.exe` and `g++.exe` are available on `PATH`

Recommended `winget` route:

```powershell
winget install --id MSYS2.MSYS2
```

Then install the UCRT64 compiler toolchain from an MSYS2 shell:

```powershell
pacman -S --needed mingw-w64-ucrt-x86_64-toolchain
```

Add the toolchain to `PATH` for the current shell:

```powershell
$env:PATH = "C:\msys64\ucrt64\bin;$env:PATH"
$env:CC = "gcc"
$env:CXX = "g++"
$env:CGO_ENABLED = "1"
```

Persist that for future Go builds if you want:

```powershell
go env -w CC=gcc
go env -w CXX=g++
go env -w CGO_ENABLED=1
```

Or just use the repo-local build script:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\build-windows.ps1 -PersistGoEnv -VerboseEnv
```

For the login window itself, install or verify WebView2:

```powershell
winget install --id Microsoft.EdgeWebView2Runtime
```

Notes:

- Windows 11 usually already includes WebView2.
- Many Windows 10 systems already have WebView2 because other apps install it.
- The final binary should not require the compiler toolchain on the target machine.
- The login command does require WebView2 on the machine where you run it.
- Go's `cgo` on Windows expects a GCC-compatible compiler; `cl.exe` is not sufficient for this project.

## Commands

```text
idrive-gateway login
idrive-gateway probe --root /
idrive-gateway smoke --root /
idrive-gateway serve --root / --listen 127.0.0.1:8787
```

## Quick start with rclone

If you already built the binary and logged in once, the shortest cross-platform path is:

1. Validate the account and write path:

```text
idrive-gateway probe --root /
```

2. Start the local WebDAV bridge with fixed credentials so your `rclone` remote stays stable:

```text
idrive-gateway serve --root / --listen 127.0.0.1:8787 --dav-user idrive --dav-pass localtest123
```

3. In another shell, create or update the `rclone` remote with plain `rclone`:

```text
rclone obscure localtest123
rclone config create idrive-local webdav url http://127.0.0.1:8787 vendor other user idrive pass <obscured-password>
```

4. Test it:

```text
rclone lsd idrive-local:
rclone ls idrive-local:
```

5. Mount it:

```text
Windows:
rclone mount idrive-local: X: --network-mode --vfs-cache-mode full --dir-cache-time 30m --attr-timeout 1m --poll-interval 0 --no-modtime

Linux/macOS:
rclone mount idrive-local: /mnt/idrive --vfs-cache-mode full --dir-cache-time 30m --attr-timeout 1m --poll-interval 0 --no-modtime
```

Notes:

- The `rclone` remote points to your local gateway, not directly to IDrive.
- The `serve` process must stay running while you use the remote or the mount.
- If you start `serve` without `--dav-user` and `--dav-pass`, it prints generated credentials; use those with the setup script or with `rclone config create`.
- You only need to create the `rclone` remote once unless the local WebDAV URL or credentials change.
- Windows convenience helper: [setup-rclone.ps1](/C:/Users/felip/code/sabino/idrive-unlimited/scripts/setup-rclone.ps1)
- POSIX shell helper for Linux/macOS: [setup-rclone.sh](/C:/Users/felip/code/sabino/idrive-unlimited/scripts/setup-rclone.sh)

## Notes

- Small uploads and chunked uploads are both implemented from the live browser traffic.
- The current web upload flow supports files up to 2 GB.
- `probe --scratch ...` treats the provided path as a disposable base name and appends a unique suffix for each run, which avoids collisions with IDrive's asynchronous copy pipeline.
- Read opens are lazy now: a file is not downloaded just because it was opened or statted.
- Downloaded file versions are cached on disk under the user cache directory and reused across later opens. Set `IDRIVE_GATEWAY_CACHE_DIR` to override the cache location.
- `smoke` starts an in-process loopback WebDAV bridge and runs real `rclone webdav` commands against it.
- `smoke --mount-drive X:` also validates a temporary Windows drive mount through `rclone mount`.
- Device pseudo-folders such as `Contacts`, `Calendar`, `Call Logs`, and `SMS` are exposed read-only.
- Device-backed top-level roots are exposed with friendly names and their root names are not renamable.

See [docs/rclone.md](/C:/Users/felip/code/sabino/idrive-unlimited/docs/rclone.md) for `rclone` usage.
