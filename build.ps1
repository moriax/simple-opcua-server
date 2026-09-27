# Cross-compiles simpleopcuaserver into dist\ for Windows and Linux.
$ErrorActionPreference = "Stop"

$Version = if ($env:VERSION) { $env:VERSION } else { Get-Date -Format "yyyy.MM.dd" }
$Out     = if ($env:OUT) { $env:OUT } else { "dist" }
$LdFlags = "-s -w -X main.version=$Version"

if (Test-Path $Out) { Remove-Item -Recurse -Force $Out }
New-Item -ItemType Directory -Path "$Out\config" | Out-Null

$env:CGO_ENABLED = "0"
foreach ($t in @(
    @{os="linux";   arch="amd64"; ext=""},
    @{os="linux";   arch="arm64"; ext=""},
    @{os="windows"; arch="amd64"; ext=".exe"},
    @{os="windows"; arch="arm64"; ext=".exe"}
)) {
    $name = "simple-opcua-server-$($t.os)-$($t.arch)$($t.ext)"
    Write-Host "building $name"
    $env:GOOS = $t.os
    $env:GOARCH = $t.arch
    go build -trimpath -ldflags $LdFlags -o "$Out\$name" ./cmd/simple-opcua-server
    if ($LASTEXITCODE -ne 0) { throw "build failed for $name" }
}

Copy-Item config\AddressSpace.example.csv "$Out\config\"
Write-Host "`nbuilt version ${Version}:"
Get-ChildItem $Out
