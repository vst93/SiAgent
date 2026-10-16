# Run in Windows PowerShell 5.1. WSL and GitHub are fixtures; no real user settings change.
$ErrorActionPreference = 'Stop'
$project = Split-Path $PSScriptRoot -Parent
$tokens = $null
$parseErrors = $null
[System.Management.Automation.Language.Parser]::ParseFile((Join-Path $project 'scripts/install.ps1'), [ref]$tokens, [ref]$parseErrors) | Out-Null
if ($parseErrors.Count) { throw ($parseErrors | Out-String) }
$root = Join-Path ([IO.Path]::GetTempPath()) ('siagent-test-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path (Join-Path $root 'System32') -Force | Out-Null
$originalRoot = $env:SystemRoot
$fixtureWSL = Join-Path $root 'System32/wsl.exe'
$originalLocal = $env:LOCALAPPDATA
$originalLog = $env:SIAGENT_WSL_TEST_LOG
try {
    $source = @'
using System;
using System.IO;
public class FakeWSL {
    public static int Main(string[] args) {
        string command = string.Join(" ", args);
        File.AppendAllText(Environment.GetEnvironmentVariable("SIAGENT_WSL_TEST_LOG"), command + "\n");
        if (command.Contains("printenv HOME")) { Console.WriteLine("/home/test-user"); return 0; }
        if (command.Contains("--install")) return 0;
        if (!command.Contains("sh -s -- --version v1.2.3 --repo vst93/SiAgent --no-modify-path")) return 2;
        string script = Console.In.ReadToEnd();
        if (!script.StartsWith("#!/bin/sh\n") || script.Contains("\r") || script[0] == '\uFEFF') return 3;
        return 0;
    }
}
'@
    # Compile without loading the fixture assembly, so Windows can delete it later.
    $compiler = New-Object Microsoft.CSharp.CSharpCodeProvider
    $options = New-Object System.CodeDom.Compiler.CompilerParameters
    $options.GenerateExecutable = $true
    $options.OutputAssembly = Join-Path $root 'System32/wsl.exe'
    try {
        $compiled = $compiler.CompileAssemblyFromSource($options, $source)
        if ($compiled.Errors.HasErrors) { throw ($compiled.Errors | Out-String) }
    }
    finally { $compiler.Dispose() }
    # Redirect only the WSL lookup; changing SystemRoot breaks Windows crypto DLL loading.
    function Join-Path {
        param([string]$Path, [string]$ChildPath)
        if ($Path -eq $originalRoot -and $ChildPath -eq 'System32\wsl.exe') { return $fixtureWSL }
        Microsoft.PowerShell.Management\Join-Path -Path $Path -ChildPath $ChildPath
    }
    $env:LOCALAPPDATA = Join-Path $root 'local app data'
    $env:SIAGENT_WSL_TEST_LOG = Join-Path $root 'wsl.log'
    $script:badChecksum = $false
    function Invoke-RestMethod { param($Uri, $Headers); return @{ tag_name = 'v1.2.3' } }
    function Invoke-WebRequest {
        param([switch]$UseBasicParsing, $Uri, $OutFile)
        if ($Uri -like '*/install.sh') {
            Copy-Item (Join-Path $project 'scripts/install.sh') $OutFile
        }
        elseif ($Uri -like '*/SHA256SUMS') {
            $hash = (Get-FileHash (Join-Path $project 'scripts/install.sh') -Algorithm SHA256).Hash
            if ($script:badChecksum) { $hash = '0' * 64 }
            return @{ Content = "$hash  ./install.sh`n" }
        }
        else { throw "Unexpected download: $Uri" }
    }
    & (Join-Path $project 'scripts/install.ps1') -NoModifyPath
    $shim = Join-Path $env:LOCALAPPDATA 'SiAgent/bin/siagent.cmd'
    $before = Get-Content -LiteralPath $shim -Raw
    if (-not $before.Contains('"/home/test-user/.local/bin/siagent" %*')) { throw 'Wrong Windows command shim' }
    if (-not $before.Contains('REM SiAgent WSL launcher')) { throw 'Missing shim ownership marker' }
    $script:badChecksum = $true
    $failed = $false
    try { & (Join-Path $project 'scripts/install.ps1') -Version v1.2.3 -NoModifyPath }
    catch { if ($_.Exception.Message -notlike '*SHA-256 mismatch*') { throw }; $failed = $true }
    if (-not $failed) { throw 'Corrupt installer checksum was accepted' }
    if ((Get-Content -LiteralPath $shim -Raw) -ne $before) { throw 'Failed install replaced the command shim' }
    & (Join-Path $project 'scripts/install.ps1') -InstallWSL
    Write-Host 'Windows installer passed: PS 5.1 syntax, SHA-256, WSL arguments, LF stdin, command shim and bootstrap path'
}
finally {
    $env:LOCALAPPDATA = $originalLocal
    $env:SIAGENT_WSL_TEST_LOG = $originalLog
    Remove-Item -LiteralPath $root -Recurse -Force
}
