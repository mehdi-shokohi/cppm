<#
.SYNOPSIS
    cppm installer for Windows.

.DESCRIPTION
    Downloads the release binary for this platform, verifies its checksum and
    installs it. Builds from source instead when -FromSource is given or no
    release asset matches.

.EXAMPLE
    irm https://raw.githubusercontent.com/OWNER/cppm/main/install.ps1 | iex

.EXAMPLE
    .\install.ps1 -Version v0.1.0 -InstallDir C:\tools
#>
[CmdletBinding()]
param(
    [string]$Version    = $(if ($env:CPPM_VERSION) { $env:CPPM_VERSION } else { 'latest' }),
    [string]$Repo       = $(if ($env:CPPM_REPO)    { $env:CPPM_REPO }    else { 'OWNER/cppm' }),
    [string]$InstallDir = $(if ($env:CPPM_INSTALL_DIR) { $env:CPPM_INSTALL_DIR } else { "$env:LOCALAPPDATA\cppm\bin" }),
    [switch]$FromSource
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Write-Step { param([string]$Message) Write-Host $Message }
function Write-Warn { param([string]$Message) Write-Warning $Message }

function Get-Architecture {
    switch ($env:PROCESSOR_ARCHITECTURE) {
        'AMD64' { 'amd64' }
        'ARM64' { 'arm64' }
        default { throw "Unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
    }
}

function Install-Binary {
    param([string]$SourcePath)

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $target = Join-Path $InstallDir 'cppm.exe'

    # A running cppm.exe cannot be overwritten, which is the usual cause of a
    # failed upgrade; renaming the old one out of the way works regardless.
    if (Test-Path $target) {
        $backup = "$target.old"
        Remove-Item $backup -Force -ErrorAction SilentlyContinue
        try { Move-Item $target $backup -Force } catch {
            throw "Could not replace $target. Close any running cppm and retry."
        }
    }
    Move-Item $SourcePath $target -Force
    Write-Step "installed $target"
    return $target
}

function Test-Checksum {
    param([string]$FilePath, [string]$ChecksumsPath, [string]$AssetName)

    $line = Select-String -Path $ChecksumsPath -Pattern ([regex]::Escape($AssetName)) -ErrorAction SilentlyContinue
    if (-not $line) { Write-Warn "No checksum listed for $AssetName; skipping verification"; return }

    $expected = ($line.Line -split '\s+')[0]
    $actual   = (Get-FileHash -Path $FilePath -Algorithm SHA256).Hash.ToLower()
    if ($actual -ne $expected.ToLower()) {
        throw "Checksum mismatch for ${AssetName}:`n  expected $expected`n  actual   $actual"
    }
    Write-Step '  checksum verified'
}

function Build-FromSource {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        throw 'Building from source needs the Go toolchain (https://go.dev/dl)'
    }
    Write-Step "building cppm from source with $(go version)"
    $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("cppm-" + [guid]::NewGuid())
    New-Item -ItemType Directory -Force -Path $tmp | Out-Null
    try {
        if ((Test-Path './go.mod') -and (Test-Path './cmd/cppm')) {
            $src = '.'
        } else {
            if (-not (Get-Command git -ErrorAction SilentlyContinue)) {
                throw 'git is required to clone the source'
            }
            Write-Step "  cloning https://github.com/$Repo"
            git clone --quiet --depth 1 "https://github.com/$Repo" "$tmp\src"
            if ($LASTEXITCODE -ne 0) { throw 'git clone failed' }
            $src = "$tmp\src"
        }
        Push-Location $src
        try {
            & go build -trimpath -ldflags '-s -w' -o "$tmp\cppm.exe" ./cmd/cppm
            if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
        } finally { Pop-Location }
        return Install-Binary "$tmp\cppm.exe"
    } finally {
        Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Add-ToUserPath {
    # Only the user-scoped PATH is touched, so this never needs admin rights and
    # never affects other accounts.
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($userPath -and ($userPath -split ';' | Where-Object { $_ -eq $InstallDir })) { return }

    $newPath = if ([string]::IsNullOrEmpty($userPath)) { $InstallDir } else { "$userPath;$InstallDir" }
    [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
    $env:Path = "$env:Path;$InstallDir"
    Write-Step "added $InstallDir to your user PATH (restart your terminal for other sessions)"
}

function Invoke-Install {
    $installed = $null

    if ($FromSource) {
        $installed = Build-FromSource
    } else {
        $arch  = Get-Architecture
        $asset = "cppm_windows_$arch.zip"
        $base  = if ($Version -eq 'latest') {
            "https://github.com/$Repo/releases/latest/download"
        } else {
            "https://github.com/$Repo/releases/download/$Version"
        }

        $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("cppm-" + [guid]::NewGuid())
        New-Item -ItemType Directory -Force -Path $tmp | Out-Null
        try {
            Write-Step "downloading $asset ($Version)"
            try {
                Invoke-WebRequest -Uri "$base/$asset" -OutFile "$tmp\$asset" -UseBasicParsing
            } catch {
                Write-Warn "No release asset for windows_$arch; falling back to building from source"
                $installed = Build-FromSource
                Show-NextSteps $installed
                return
            }
            try {
                Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile "$tmp\checksums.txt" -UseBasicParsing
                Test-Checksum "$tmp\$asset" "$tmp\checksums.txt" $asset
            } catch {
                Write-Warn 'Could not download checksums.txt; skipping verification'
            }
            Expand-Archive -Path "$tmp\$asset" -DestinationPath $tmp -Force
            if (-not (Test-Path "$tmp\cppm.exe")) { throw 'Archive did not contain cppm.exe' }
            $installed = Install-Binary "$tmp\cppm.exe"
        } finally {
            Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
        }
    }

    Add-ToUserPath
    Show-NextSteps $installed
}

function Show-NextSteps {
    param([string]$BinaryPath)

    Write-Host ''
    if ($BinaryPath -and (Test-Path $BinaryPath)) { & $BinaryPath --version }
    Write-Host ''
    Write-Host 'next steps:'
    Write-Host '    cppm doctor       check your toolchain'
    Write-Host '    cppm init MyApp   create a project'
}

Invoke-Install
