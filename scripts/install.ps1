# Windows installs SiAgent inside WSL and adds a Windows command shim.
[CmdletBinding()]
param(
    [string]$Version = 'latest',
    [ValidatePattern('\A[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\z')]
    [string]$Repository = 'vst93/SiAgent',
    [ValidatePattern('\A[A-Za-z0-9_.-]+\z')]
    [string]$Distribution = 'Ubuntu',
    [switch]$InstallWSL,
    [switch]$NoModifyPath
)

$ErrorActionPreference = 'Stop'
if ($env:OS -ne 'Windows_NT') { throw 'Use scripts/install.sh on Linux or macOS.' }
$wsl = Join-Path $env:SystemRoot 'System32\wsl.exe'
if (-not (Test-Path $wsl)) { throw 'WSL is unavailable. Update Windows and install WSL: https://aka.ms/wslinstall' }
if ($InstallWSL) {
    & $wsl --install -d $Distribution --no-launch
    if ($LASTEXITCODE -ne 0) { throw 'WSL setup failed. Run PowerShell as Administrator and retry with -InstallWSL.' }
    Write-Host 'Restart Windows if requested, then open the WSL distribution once to create its Linux user.'
    Write-Host 'After initialization, rerun this installer without -InstallWSL.'
    return
}
$linuxHome = ((& $wsl -d $Distribution --exec printenv HOME 2>$null) | Out-String).Trim()
if ($LASTEXITCODE -ne 0 -or -not $linuxHome.StartsWith('/')) {
    throw "Initialize WSL first: run this script with -InstallWSL from Administrator PowerShell, restart if requested, then run 'wsl -d $Distribution' to create a Linux user."
}
if ($linuxHome -match '["%\r\n]') { throw 'Unsupported WSL home path for the Windows command shim.' }
# PS 5.1 may otherwise negotiate an obsolete TLS version.
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
if ($Version -eq 'latest') {
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repository/releases/latest" -Headers @{ 'User-Agent' = 'SiAgent-Installer' }
    $Version = $release.tag_name
}
if ($Version -notmatch '\Av[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?\z') { throw 'Version must be a release tag, such as v0.1.0.' }
$url = "https://github.com/$Repository/releases/download/$Version"
$temporary = Join-Path ([IO.Path]::GetTempPath()) ('siagent-install-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $temporary | Out-Null
try {
    $installer = Join-Path $temporary 'install.sh'
    Invoke-WebRequest -UseBasicParsing -Uri "$url/install.sh" -OutFile $installer
    $manifest = (Invoke-WebRequest -UseBasicParsing -Uri "$url/SHA256SUMS").Content
    $matchesForScript = @($manifest -split "`n" | Where-Object { $_ -match '^[0-9a-fA-F]{64}\s+\*?(\./)?install\.sh\s*$' })
    if ($matchesForScript.Count -ne 1) { throw 'Missing or ambiguous SHA-256 for install.sh.' }
    $expected = ($matchesForScript[0] -split '\s+')[0]
    if ((Get-FileHash -LiteralPath $installer -Algorithm SHA256).Hash -ne $expected) { throw 'SHA-256 mismatch for install.sh.' }
    # Write UTF-8/LF directly: Windows PowerShell's native pipeline adds CRLF.
    $scriptText = [IO.File]::ReadAllText($installer).Replace("`r`n", "`n")
    $start = New-Object System.Diagnostics.ProcessStartInfo
    $start.FileName = $wsl
    $start.Arguments = '-d "{0}" --exec sh -s -- --version "{1}" --repo "{2}" --no-modify-path' -f $Distribution, $Version, $Repository
    $start.UseShellExecute = $false
    $start.RedirectStandardInput = $true
    $process = [Diagnostics.Process]::Start($start)
    try {
        $bytes = [Text.Encoding]::UTF8.GetBytes($scriptText)
        $process.StandardInput.BaseStream.Write($bytes, 0, $bytes.Length)
        $process.StandardInput.BaseStream.Close()
        $process.WaitForExit()
        if ($process.ExitCode -ne 0) { throw 'Installation inside WSL failed; see the Linux installer output above.' }
    }
    finally { $process.Dispose() }

    $bin = Join-Path $env:LOCALAPPDATA 'SiAgent\bin'
    New-Item -ItemType Directory -Force -Path $bin | Out-Null
    $shim = Join-Path $bin 'siagent.cmd'
    if ((Test-Path $shim) -and -not ((Get-Content -LiteralPath $shim -Raw).Contains('REM SiAgent WSL launcher'))) {
        throw "An unrelated command already exists: $shim"
    }
    $command = "@echo off`r`nREM SiAgent WSL launcher`r`n`"%SystemRoot%\System32\wsl.exe`" -d `"$Distribution`" --exec `"$linuxHome/.local/bin/siagent`" %*`r`nexit /b %errorlevel%`r`n"
    [IO.File]::WriteAllText($shim, $command, [Text.Encoding]::Default)
    if (-not $NoModifyPath) {
        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        if (@($userPath -split ';') -notcontains $bin) {
            [Environment]::SetEnvironmentVariable('Path', ($bin + ';' + $userPath), 'User')
        }
        if (@($env:Path -split ';') -notcontains $bin) { $env:Path = $bin + ';' + $env:Path }
    }
    Write-Host "Installed Windows command: $shim"
    Write-Host 'Open a new terminal and run: siagent'
    Write-Host 'Data is stored inside WSL; command arguments use Linux paths.'
    Write-Host 'Web: http://127.0.0.1:8080'
}
finally {
    Remove-Item -LiteralPath $temporary -Recurse -Force
}
