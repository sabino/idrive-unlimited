# rclone usage

## Fast path

If you want the shortest working setup:

1. Start the gateway with fixed local WebDAV credentials:

```text
idrive-gateway serve --root / --listen 127.0.0.1:8787 --dav-user idrive --dav-pass localtest123
```

2. In another shell, create the `rclone` remote with plain `rclone`:

```text
rclone obscure localtest123
rclone config create idrive-local webdav url http://127.0.0.1:8787 vendor other user idrive pass <obscured-password>
```

3. Test it:

```text
rclone lsd idrive-local:
```

4. Mount it:

```text
Windows:
rclone mount idrive-local: X: --network-mode --vfs-cache-mode full --dir-cache-time 30m --attr-timeout 1m --poll-interval 0 --no-modtime

Linux/macOS:
rclone mount idrive-local: /mnt/idrive --vfs-cache-mode full --dir-cache-time 30m --attr-timeout 1m --poll-interval 0 --no-modtime
```

The `serve` process must stay running while `rclone` uses the remote.

## 1. Login

```powershell
idrive-gateway login
```

This opens the lightweight native login window when the binary was built with `CGO_ENABLED=1`.

## 2. Probe the account

```powershell
idrive-gateway probe --root / --scratch /.idrive-gateway-probe
```

The probe treats `--scratch` as a disposable base name, appends a unique suffix for that run, uploads a small file, uploads a chunked file, renames, copies, moves, downloads, deletes, and then cleans everything up.

## 3. Start the local WebDAV bridge

```powershell
idrive-gateway serve --root / --listen 127.0.0.1:8787
```

The server prints the generated local WebDAV username and password.

## 4. Configure rclone

Interactive:

```powershell
rclone config
```

Non-interactive:

```powershell
rclone config create idrive-local webdav url http://127.0.0.1:8787 vendor other user idrive pass <obscured-password>
```

Helper script:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\setup-rclone.ps1 -RemoteName idrive-local -Url http://127.0.0.1:8787 -User idrive -Password localtest123
```

POSIX helper:

```bash
./scripts/setup-rclone.sh --remote-name idrive-local --url http://127.0.0.1:8787 --user idrive --password localtest123
```

## 5. Common commands

List the Cloud Backup root:

```powershell
rclone lsd idrive-local:
rclone ls idrive-local:
```

Create a directory:

```powershell
rclone mkdir idrive-local:/new-folder
```

Upload a file:

```powershell
rclone copyto .\local-file.bin idrive-local:/Uploads/local-file.bin
```

Move a file:

```powershell
rclone moveto idrive-local:/Uploads/local-file.bin idrive-local:/Uploads/local-file-renamed.bin
```

Delete a file:

```powershell
rclone delete idrive-local:/Uploads/local-file-renamed.bin
```

Mount:

```powershell
rclone mount idrive-local: X: --network-mode --vfs-cache-mode full --dir-cache-time 30m --attr-timeout 1m --poll-interval 0 --no-modtime
```

## 6. Built-in smoke test

Run a disposable end-to-end `rclone webdav` check without managing a separate `serve` process yourself:

```powershell
idrive-gateway smoke --root /
```

To also verify that your Windows `rclone mount` setup works:

```powershell
idrive-gateway smoke --root / --mount-drive X:
```

## Behavior notes

- The exposed root is IDrive Cloud Backup `/`.
- Device-backed folders under `/` are traversable.
- File downloads are lazy. Listing or statting a file does not download the file body.
- Downloaded file versions are cached on disk by the gateway and reused on later opens.
- Set `IDRIVE_GATEWAY_CACHE_DIR` to override the default cache location.
- `Contacts`, `Calendar`, `Call Logs`, and `SMS` are exposed read-only.
- The implemented web upload flow matches the live browser client and is currently capped at 2 GB per file.
