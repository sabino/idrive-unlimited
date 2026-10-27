# IDrive gateway audit and live mount verification, 2026-10-27

Remediation update: the subsequent safety change addresses the seven reproduced defects and the additional concrete maintenance issues. See [write safety and local operation](safety.md) for the resulting behavior and remaining service limits. The findings and baseline results below describe revision `df08f6b`, before those fixes. The reproduction patch is a historical fixture for that revision; current regression tests live alongside the implementation.

At the audited revision, the Linux build worked with the authenticated IDrive account: native login, EVS operations, rclone WebDAV operations, and an actual FUSE mount all passed. That revision had write-safety and cache-isolation defects and was kept read-only. The subsequent fixes and current behavior are described in the linked safety and reliability reviews.

Reviewed revision: `df08f6b`. Application source and dependencies were left unchanged. This report and the accompanying reproduction patch are the only repository additions.

## What was verified

| Check | Result |
| --- | --- |
| Existing tests, headless build configuration | All 12 tests passed |
| `go vet ./...`, headless configuration | Passed |
| `gofmt -l .`, original application source | No unformatted files |
| Existing tests, native configuration with race detection | All 12 tests passed; no race reported in exercised paths |
| Native Linux packaging script | Built successfully using its WebKitGTK 4.1 compatibility shim |
| Native embedded login | User signed in directly in the login window; token exchange and saved-session validation succeeded |
| Live EVS `probe --root / --json` | All 18 steps passed: small and chunked uploads, rename, copy, move, download checksum, and cleanup |
| Live `smoke --root /` | rclone listing, mkdir, upload, read, move, delete, and purge passed |
| Friendly device-backed root traversal | rclone listed one device root through its friendly alias and returned nine immediate entries |
| Actual `rclone mount` | FUSE filesystem mounted successfully, with kernel mount option `ro` |
| Read through mounted filesystem | 131,393-byte disposable file matched its local SHA-256 hash |
| Read-only behavior | Write attempt in the disposable directory failed with `EROFS` |
| Gateway authentication | An unauthenticated HTTP request returned 401 |
| Remote cleanup | Probe, smoke, and mount-test scratch data created in this audit were removed; final root listing showed no audit scratch leftovers |
| `govulncheck`, headless and native source configurations | No known reachable vulnerabilities found; 13 module-level advisories in unused portions of the pinned `golang.org/x/net` dependency |
| Additional isolated fault-injection tests | Seven tests failed, reproducing the seven defects below |

Test coverage is 11.3% of statements: EVS 4.2%, session 49.3%, WebDAV 33.2%, and CLI/auth/probe 0%. The existing green suite therefore provides little assurance about failed uploads, authentication expiry, asynchronous mutations, or interrupted requests.

The audit used Go 1.26.10, rclone 1.75.2, GTK 3.24.52, and WebKitGTK 2.54.1. Tool archives were downloaded from their official distribution sources and checked against their published SHA-256 checksums. Platform runtime tests were Linux-only; Windows/macOS installers, long-running session expiry, concurrent production workloads, and files near or above the documented 2 GB upload limit were not exercised.

## Confirmed defects, in recommended repair order

### 1. High: failed replacement uploads delete the original file

Location: `internal/evs/client.go:443`, `UploadFile`; `Delete` uses `trash=no` at line 373.

For an existing file, `UploadFile` permanently deletes the destination before it starts the replacement upload. An upload failure therefore leaves the destination missing. The local WebDAV staging file is also removed unconditionally after the attempted upload (`internal/webdav/fs.go:443`), eliminating that recovery copy.

Reproduction: an existing `/old.txt` is returned by browse; the delete API succeeds; the upload endpoint returns HTTP 503. The operation reports failure, but the original has already been deleted.

Improvement: validate the upload before altering the destination, stage the replacement at a unique remote path, verify it, and replace using the strongest operation EVS supports. If EVS cannot replace atomically, preserve a recoverable original and document the remaining failure window. Keep failed local staging files for an explicit, bounded recovery period. Do not assume an atomic EVS replacement exists without checking the service behavior.

### 2. High: an interrupted WebDAV PUT commits partial content

Location: `internal/webdav/fs.go:441`, `writeFile.Close`.

The WebDAV handler calls `Close` even when reading the incoming request body fails. This implementation treats closing the temporary file as permission to upload it, using `context.Background()` regardless of the request state. An interrupted upload can thus create a truncated remote file or replace an existing file with truncated data.

Reproduction: a PUT declares 100 bytes, supplies `partial`, then returns `io.ErrUnexpectedEOF`. The HTTP response is 405, yet the EVS mock receives and commits the seven-byte partial file.

Improvement: separate successful request completion from closing a file. Commit only after the body was read successfully and the received length is valid when known; discard an aborted body. A request-aware PUT wrapper or explicit commit/abort staging layer is needed because the generic filesystem `Close` callback does not receive the body-copy error. Simply retaining the context does not cover every body-read failure.

### 3. High: persistent download cache can serve another account's data

Location: `internal/webdav/fs.go:29`; cache version key in `internal/webdav/cache.go:107`.

The cache namespace contains only the server hostname and mounted root. It has no account or session identity. Two accounts using the same IDrive server and local cache directory can share cached content when path, size, modification time, and version match.

Reproduction: two clients with different synthetic EVS sessions on the same host access the same five-byte file metadata. Account B receives account A's cached `ALICE` bytes without performing its own download.

Improvement: include a stable, verified account identifier in the cache namespace. Until one is available, isolate caches by a nonreversible session-specific identifier, accepting reduced reuse across logins. Invalidate the old namespace on an account switch. The reproduction uses synthetic accounts; a second real account was not accessed.

### 4. Medium: copy/move completion polling reuses stale metadata

Location: `internal/evs/client.go:310`, `Copy`; line 334, `Move`; line 937, `waitForPathState`.

Completion polling calls `Stat`, which consults the five-minute browse cache. Destination/source invalidation occurs only after polling succeeds. A previously cached empty destination, or the first browse before an asynchronous copy becomes visible, can therefore cause a 45-second timeout even though the server completed the operation. A stale source entry can similarly prevent move completion from being observed.

Reproduction: warm the destination listing as empty, complete the copy upstream, and then poll. The operation hits the test deadline with just one browse call: it never refreshes the listing. The live probe succeeded because its observed service timing did not trigger this condition.

Improvement: invalidate affected parents before waiting and bypass cached listings on every completion poll. Check the actual expected transition, including the case where a destination already existed. Invalidate cached descendants after directory moves/deletions as well.

### 5. Medium: unexpected HTTP 200 responses can count as successful mutations

Location: `internal/evs/client.go:787`, `checkMaybeJSONReply`.

Empty bodies, non-JSON bodies, malformed JSON, and objects without a `message` are accepted as success. This helper is used for uploads and raw mutation responses. An HTTP 200 login page after a session-expiry redirect can therefore be mistaken for a successful operation.

Reproduction: an HTML login form passed to the actual response validator returns no error. No real session-expiry failure was induced in the account.

Improvement: define accepted response formats per endpoint, reject authentication redirects and HTML, and validate upload finalization against fresh remote metadata or a checksum. Preserve any genuinely required empty-response compatibility only for endpoints where it has been demonstrated.

### 6. Medium: session export loses cookie identity and attributes

Location: `internal/session/store.go:131`, `FromCookieJar`.

`cookiejar.Jar.Cookies` provides request cookies rather than a complete persistence representation. Exporting these as if they retained original paths/domains/expiry loses that metadata. Deduplication by name plus the missing path/domain then collapses distinct cookies with the same name.

Reproduction: a root `SESSION=root` cookie and an `/evs/` `SESSION=evs` cookie become only `SESSION=root` after export/import. The existing tests exercise distinct names and counts, so they miss this loss. The current live account's saved session did work.

Improvement: persist original `Set-Cookie` attributes through an audited persistent jar or response-cookie recorder, preserving host-only scope, path, expiry, deletion, and duplicate-name behavior. Add round-trip tests for these cases rather than only cookie counts.

### 7. Medium: saving over an existing session does not restrict its permissions

Location: `internal/session/store.go:79`, `Store.Save`.

`os.WriteFile(..., 0600)` applies that mode when creating a file; it leaves an existing file's broader mode intact. A custom or previously created world-readable session file remains world-readable after authentication cookies are saved. The direct write also lacks atomic replacement.

Reproduction: create `session.json` with mode 0644, save a session, and observe that it remains 0644. This is a Unix permissions finding; the actual session created in this audit is correctly 0600 inside a 0700 directory.

Improvement: write to a new 0600 temporary file in the same directory, close it successfully, and atomically replace the session file. Handle platform-specific replacement/ACL behavior and avoid following a destination symlink. Test replacement failure as well as mode enforcement.

## Additional clear improvements from source/build inspection

| Priority | Finding | Concrete improvement |
| --- | --- | --- |
| Medium | `serve` describes itself as loopback-only, but `--listen` is passed directly to `http.Server.Addr` without validation (`cmd/idrive-gateway/main.go:155`, 187). A wildcard/LAN address can expose the bridge over plaintext HTTP. | Validate loopback addresses before login or startup. Reject wildcard and non-loopback binds by default. The running audit server is bound to `127.0.0.1`. |
| Medium | A fresh-cache native build using the README's plain `CGO_ENABLED=1 go build ...` fails on this WebKitGTK-4.1-only host because the pinned `webview_go` version explicitly requests `webkit2gtk-4.0`. The packaging script already works around this. | Make the supported build helper the documented default, or update the dependency/build integration to support 4.1 explicitly. Share the dependency setup between build, tests, and packaging; clean up temporary pkg-config shims. |
| Medium | Every read first downloads the entire file, with a two-minute HTTP client timeout. The persistent gateway cache has no eviction/size policy and can duplicate rclone's full VFS cache. | Add cache size/age limits and maintenance, propagate request cancellation, and assess ranged/streaming reads against real EVS behavior. Use separate metadata and transfer timeout policies. |
| Medium | The documented 2 GB upload limit has no matching early size guard in `UploadFile`. | Enforce the verified service limit before staging excessive data or deleting/replacing anything, and return a useful WebDAV status. Verify whether the service boundary uses decimal GB or GiB. |
| Low | The probe does not hash-check the copied small file, and its cleanup verification treats any `Stat` error as successful removal (`internal/probe/probe.go:191`). The smoke test lacks deferred remote cleanup after an intermediate failure. | Verify the small-file hash; accept cleanup only on `ErrNotFound`; record other failures. Always attempt cleanup with an independent bounded context and report leftovers. |
| Low | The login JavaScript accepts `redirect_url`, but token exchange requires `payload.URL`; CLI auto-login also discards session-load/validation diagnostics. | Normalize the supported token URL fields, validate HTTPS and trusted origins, preserve diagnostic errors, and add an explicit noninteractive mode. |
| Maintenance | `golang.org/x/net v0.52.0` has 13 advisories in portions outside the scanned import/call graph. | Update to a compatible patched release, rerun native/headless checks and live smoke, and add dependency scanning to CI. This scan did not establish an exploitable vulnerability in the gateway. |

[Govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) reports reachability for a particular build configuration, not complete application security. Its current advisory output includes fixes through [`golang.org/x/net v0.60.0`](https://pkg.go.dev/golang.org/x/net@v0.60.0), for example [GO-2026-6617](https://pkg.go.dev/vuln/GO-2026-6617). These are maintenance findings here, rather than demonstrated reachable bugs.

## Reproducing the seven defects without touching the account

The adjacent `audit-2026-10-27-repros.patch` adds isolated regression tests using only temporary local files and HTTP fixtures. It is deliberately not applied to the working source because these tests currently fail. Each failure asserts the safer expected behavior and demonstrates the corresponding finding above.

Apply it only in a disposable checkout of the audited revision `df08f6b`, then run:

```sh
git apply docs/audit-2026-10-27-repros.patch
CGO_ENABLED=0 go test ./internal/... -run TestAudit -count=1 -v
```

The audit ran these tests in `/tmp/idrive-gateway-audit/source`; all seven failures were observed. The patch itself also passed `git apply --check` against the reviewed checkout.

## Local installation and running mount

- Native gateway: `/home/sabino/.local/bin/idrive-gateway` (audit build of the reviewed source).
- rclone: `/home/sabino/.local/bin/rclone`.
- Saved EVS session: `/home/sabino/.config/idrive-gateway/session.json`, mode 0600. The password was entered directly by the user in the native window and was not provided to the agent.
- Read-only mount: `/home/sabino/mnt/idrive-audit`.
- Runtime configuration, logs, build tools, reproduction copy, and check results: `/tmp/idrive-gateway-audit`, mode 0700. The rclone config and gateway log contain local bridge credentials and are private; they are not included in this report.

The gateway and mount were left running for inspection. No boot service was installed. Runtime files under `/tmp` and the running mount should be treated as temporary; the installed executables and saved session persist independently.

To unmount:

```sh
fusermount3 -u /home/sabino/mnt/idrive-audit
```

To inspect the current loopback address or process IDs without revealing the local bridge password, read `/tmp/idrive-gateway-audit/gateway-state.json` and `/tmp/idrive-gateway-audit/mount-state.json`.
