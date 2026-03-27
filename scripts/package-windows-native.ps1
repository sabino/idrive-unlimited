param(
    [string]$Version = "0.0.0-dev",
    [string]$Commit = "none",
    [string]$BuildDate = "",
    [string]$ArtifactDir = ".\\dist-native",
    [string]$BootstrapperUrl = "https://go.microsoft.com/fwlink/p/?LinkId=2124703",
    [string]$InnoSetupCompiler = "",
    [switch]$RequireInstaller
)

$ErrorActionPreference = "Stop"

function Resolve-MsysToolchainBin {
    $candidates = @(
        "C:\msys64\ucrt64\bin",
        "C:\msys64\mingw64\bin"
    )

    foreach ($candidate in $candidates) {
        $gcc = Join-Path $candidate "gcc.exe"
        $gxx = Join-Path $candidate "g++.exe"
        if ((Test-Path $gcc) -and (Test-Path $gxx)) {
            return $candidate
        }
    }

    throw "Could not find an MSYS2 GCC toolchain. Expected gcc.exe and g++.exe under C:\msys64\ucrt64\bin or C:\msys64\mingw64\bin."
}

function Ensure-PathContains([string]$dir) {
    $parts = $env:PATH -split ';' | Where-Object { $_ -ne "" }
    if ($parts -notcontains $dir) {
        $env:PATH = "$dir;$env:PATH"
    }
}

function Copy-RuntimeDlls([string]$toolchainBin, [string]$destinationDir) {
    $dlls = @(
        "libstdc++-6.dll",
        "libgcc_s_seh-1.dll",
        "libwinpthread-1.dll"
    )

    foreach ($dll in $dlls) {
        Copy-Item (Join-Path $toolchainBin $dll) (Join-Path $destinationDir $dll) -Force
    }
}

function Resolve-InnoSetupCompiler([string]$explicitPath) {
    if (-not [string]::IsNullOrWhiteSpace($explicitPath)) {
        return $explicitPath
    }

    $command = Get-Command iscc.exe -ErrorAction SilentlyContinue
    if ($command) {
        return $command.Source
    }

    $candidates = @(
        "C:\Program Files (x86)\Inno Setup 6\ISCC.exe",
        "C:\Program Files\Inno Setup 6\ISCC.exe"
    )

    foreach ($candidate in $candidates) {
        if (Test-Path $candidate) {
            return $candidate
        }
    }

    return ""
}

function Normalize-Version([string]$value) {
    return $value.TrimStart("v")
}

if ([string]::IsNullOrWhiteSpace($BuildDate)) {
    $BuildDate = [DateTime]::UtcNow.ToString("yyyy-MM-ddTHH:mm:ssZ")
}

$normalizedVersion = Normalize-Version $Version
$artifactRoot = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($ArtifactDir)
$artifactBase = "idrive-gateway_${normalizedVersion}_windows_amd64_native"
$stageDir = Join-Path $artifactRoot $artifactBase
$zipPath = Join-Path $artifactRoot "${artifactBase}.zip"
$installerBase = "idrive-gateway-setup_${normalizedVersion}_windows_amd64"

New-Item -ItemType Directory -Force -Path $artifactRoot | Out-Null
if (Test-Path $stageDir) {
    Remove-Item $stageDir -Recurse -Force
}
New-Item -ItemType Directory -Force -Path $stageDir | Out-Null

$toolchainBin = Resolve-MsysToolchainBin
Ensure-PathContains $toolchainBin
$env:CC = "gcc"
$env:CXX = "g++"
$env:CGO_ENABLED = "1"

$binaryPath = Join-Path $stageDir "idrive-gateway.exe"
$ldflags = "-s -w -X main.version=$normalizedVersion -X main.commit=$Commit -X main.date=$BuildDate"
go build -ldflags $ldflags -o $binaryPath .\cmd\idrive-gateway

Copy-RuntimeDlls -toolchainBin $toolchainBin -destinationDir $stageDir
Copy-Item .\README.md (Join-Path $stageDir "README.md") -Force
Copy-Item .\LICENSE (Join-Path $stageDir "LICENSE") -Force
Invoke-WebRequest -Uri $BootstrapperUrl -OutFile (Join-Path $stageDir "MicrosoftEdgeWebView2Setup.exe")

if (Test-Path $zipPath) {
    Remove-Item $zipPath -Force
}
Compress-Archive -Path $stageDir -DestinationPath $zipPath -CompressionLevel Optimal

$iscc = Resolve-InnoSetupCompiler $InnoSetupCompiler
if ([string]::IsNullOrWhiteSpace($iscc)) {
    if ($RequireInstaller) {
        throw "Could not find ISCC.exe. Install Inno Setup 6 or pass -InnoSetupCompiler."
    }
    Write-Host "Skipping installer build because ISCC.exe was not found."
} else {
    & $iscc "/Qp" "/DAppVersion=$normalizedVersion" "/DSourceDir=$stageDir" "/DOutputDir=$artifactRoot" "/DOutputBaseFilename=$installerBase" ".\packaging\windows\idrive-gateway.iss"
}

Write-Host "Built native Windows package:"
Write-Host "  $zipPath"
if (Test-Path (Join-Path $artifactRoot "$installerBase.exe")) {
    Write-Host "  $(Join-Path $artifactRoot "$installerBase.exe")"
}
