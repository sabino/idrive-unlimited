param(
    [string]$RemoteName = "idrive-local",
    [string]$Url = "http://127.0.0.1:8787",
    [string]$User = "idrive",
    [Parameter(Mandatory = $true)]
    [string]$Password
)

$ErrorActionPreference = "Stop"

if (-not (Get-Command rclone -ErrorAction SilentlyContinue)) {
    throw "rclone was not found on PATH."
}

$obscured = (& rclone obscure $Password).Trim()
if ([string]::IsNullOrWhiteSpace($obscured)) {
    throw "rclone obscure returned an empty password."
}

$args = @(
    "config", "create", $RemoteName, "webdav",
    "url", $Url,
    "vendor", "other",
    "user", $User,
    "pass", $obscured
)

& rclone @args

Write-Host ""
Write-Host "Configured rclone remote '$RemoteName' for $Url"
Write-Host "Next commands:"
Write-Host "  rclone lsd ${RemoteName}:"
Write-Host "  rclone mount ${RemoteName}: X: --network-mode --vfs-cache-mode full --dir-cache-time 30m --attr-timeout 1m --poll-interval 0 --no-modtime"
