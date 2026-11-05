param([switch]$Help)

$ErrorActionPreference = 'Stop'
$Version = if ($env:AGENTPACK_VERSION) { $env:AGENTPACK_VERSION } else { '@AGENTPACK_VERSION@' }
$Repository = if ($env:AGENTPACK_REPOSITORY) { $env:AGENTPACK_REPOSITORY } else { 'olegpustovit/agentpack' }

if ($Help) {
    Write-Output @"
agentpack-installer.ps1

Download, verify, and install agentpack $Version.

Options:
  -Help   Show this help.

Environment:
  AGENTPACK_VERSION, AGENTPACK_DOWNLOAD_URL, AGENTPACK_INSTALL_DIR,
  AGENTPACK_GITHUB_TOKEN, AGENTPACK_RESTART_SERVICE=1
"@
    exit 0
}

if ($env:AGENTPACK_DOWNLOAD_URL) {
    $BaseUrl = $env:AGENTPACK_DOWNLOAD_URL.TrimEnd('/')
} elseif ($env:INSTALLER_DOWNLOAD_URL) {
    $BaseUrl = $env:INSTALLER_DOWNLOAD_URL.TrimEnd('/')
} elseif ($env:AGENTPACK_INSTALLER_GHE_BASE_URL) {
    $BaseUrl = "$($env:AGENTPACK_INSTALLER_GHE_BASE_URL.TrimEnd('/'))/$Repository/releases/download/v$Version"
} elseif ($env:AGENTPACK_INSTALLER_GITHUB_BASE_URL) {
    $BaseUrl = "$($env:AGENTPACK_INSTALLER_GITHUB_BASE_URL.TrimEnd('/'))/$Repository/releases/download/v$Version"
} else {
    $BaseUrl = "https://github.com/$Repository/releases/download/v$Version"
}

if ($env:AGENTPACK_INSTALL_DIR) {
    $InstallDir = $env:AGENTPACK_INSTALL_DIR
} else {
    $InstallDir = Join-Path $HOME '.local\bin'
}

$Archive = "agentpack_${Version}_windows_amd64.zip"
$Temporary = Join-Path ([IO.Path]::GetTempPath()) ("agentpack-install-" + [guid]::NewGuid())
$Headers = @{}
if ($env:AGENTPACK_GITHUB_TOKEN) { $Headers.Authorization = "Bearer $($env:AGENTPACK_GITHUB_TOKEN)" }

try {
    New-Item -ItemType Directory -Path $Temporary | Out-Null
    $ArchivePath = Join-Path $Temporary $Archive
    $ChecksumsPath = Join-Path $Temporary 'checksums.txt'
    Write-Output "Downloading agentpack $Version (windows/amd64)"
    Invoke-WebRequest -UseBasicParsing -Headers $Headers -Uri "$BaseUrl/$Archive" -OutFile $ArchivePath
    Invoke-WebRequest -UseBasicParsing -Headers $Headers -Uri "$BaseUrl/checksums.txt" -OutFile $ChecksumsPath
    $ChecksumLine = Get-Content $ChecksumsPath | Where-Object { $_ -match "\s\*?$([regex]::Escape($Archive))$" } | Select-Object -First 1
    if (-not $ChecksumLine) { throw "checksum for $Archive is missing" }
    $Expected = ($ChecksumLine -split '\s+')[0].ToLowerInvariant()
    $Actual = (Get-FileHash -Algorithm SHA256 $ArchivePath).Hash.ToLowerInvariant()
    if ($Actual -ne $Expected) { throw "checksum mismatch for $Archive" }

    $Unpacked = Join-Path $Temporary 'unpacked'
    Expand-Archive -Path $ArchivePath -DestinationPath $Unpacked
    $Binary = Join-Path $Unpacked 'ap.exe'
    if (-not (Test-Path -LiteralPath $Binary -PathType Leaf)) { throw "ap.exe is missing from $Archive" }
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $Destination = Join-Path $InstallDir 'ap.exe'
    $Pending = "$Destination.new"
    Copy-Item -Force $Binary $Pending
    # Windows can rename a running executable but not replace it, and an agent
    # launched by agentpack keeps it running. Old copies go once nothing runs them.
    Remove-Item -Force "$Destination.*.old" -ErrorAction SilentlyContinue
    if (Test-Path -LiteralPath $Destination) { Move-Item -Force $Destination "$Destination.$([guid]::NewGuid().ToString('N')).old" }
    Move-Item -Force $Pending $Destination

    $AgentpackAlias = Join-Path $InstallDir 'agentpack.exe'
    if ((Test-Path -LiteralPath $AgentpackAlias -PathType Leaf) -and -not (Get-Item -LiteralPath $AgentpackAlias).LinkType) {
        $AliasPending = "$AgentpackAlias.new"
        Copy-Item -Force $Destination $AliasPending
        if (Test-Path -LiteralPath $AgentpackAlias) { Move-Item -Force $AgentpackAlias "$AgentpackAlias.$([guid]::NewGuid().ToString('N')).old" }
        Move-Item -Force $AliasPending $AgentpackAlias
    }

    if ($env:AGENTPACK_RESTART_SERVICE -eq '1') {
        & $Destination service start
        if ($LASTEXITCODE -ne 0) { throw 'Could not restart the Agentpack gateway service' }
    }

    Write-Output "Installed agentpack $Version to $Destination"
} finally {
    if (Test-Path $Temporary) { Remove-Item -Recurse -Force $Temporary }
}
