#!/usr/bin/env sh
# Cross-compiles simpleopcuaserver into dist/ for Windows and Linux.
# CGO is off, so every binary is statically linked and has no runtime deps.
set -eu

VERSION="${VERSION:-$(date +%Y.%m.%d)}"
OUT="${OUT:-dist}"
LDFLAGS="-s -w -X main.version=${VERSION}"

rm -rf "$OUT"
mkdir -p "$OUT"

build() {
	goos="$1"
	goarch="$2"
	ext="$3"
	name="simple-opcua-server-${goos}-${goarch}${ext}"
	printf 'building %s\n' "$name"
	CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
		go build -trimpath -ldflags "$LDFLAGS" -o "$OUT/$name" ./cmd/simple-opcua-server
}

build linux   amd64 ""
build linux   arm64 ""
build windows amd64 ".exe"
build windows arm64 ".exe"

# The tag file is read at runtime, so the example ships next to the binaries.
mkdir -p "$OUT/config"
cp config/AddressSpace.example.csv "$OUT/config/"

printf '\n%s\n' "built version ${VERSION}:"
ls -la "$OUT"
