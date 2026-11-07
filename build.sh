#!/bin/sh
# build.sh — sync embedded assets, build and package ee-setup.
#
# Usage:
#   ./build.sh assets          # refresh assets from the main repo
#   ./build.sh build [GOOS]    # build for the host or a named GOOS
#   ./build.sh release         # build and package dist/ for the release matrix
set -e

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
ASSETS="$HERE/internal/ee71/assets"
VERSION="${VERSION:-dev}"
LDFLAGS="-s -w -X main.version=$VERSION"

do_assets() {
    if [ ! -d "$REPO/github/dropbear" ]; then
        echo "[*] Standalone checkout: using the committed assets"
        return
    fi
    echo "[*] Syncing EE71 assets from $REPO"
    cp "$REPO/github/dropbear/files/dropbearmulti-2014.66" "$ASSETS/"
    cp "$REPO/github/dropbear/files/dropbear"              "$ASSETS/dropbear.init"
    cp "$REPO/github/dropbear/files/sftp-server"           "$ASSETS/"
    cp "$REPO/github/ee71-update/internal/bundle/assets/patch_usb_kernel" "$ASSETS/"
    echo "[*] Assets synced ($(du -sh "$ASSETS" | cut -f1))"
}

do_build() {
    goos="${1:-$(go env GOOS)}"
    echo "[*] Building for $goos ($VERSION)"
    out="$HERE/ee-setup"
    [ "$goos" = windows ] && out="$out.exe"
    if [ "$goos" = windows ]; then
        CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$LDFLAGS" -o "$out" "$HERE"
    else
        CGO_ENABLED=1 GOOS="$goos" go build -trimpath -ldflags "$LDFLAGS" -o "$out" "$HERE"
    fi
    echo "[*] Built: $out"
}

# package <goos> <goarch> <cgo:0|1> <version>
package() {
    goos="$1"; goarch="$2"; cgo="$3"
    name="ee-setup-$VERSION-$goos-$goarch"
    dir="$HERE/dist/$name"
    echo "[*] $name (cgo=$cgo)"
    rm -rf "$dir"; mkdir -p "$dir"
    if [ "$goos" = windows ]; then
        ext=".exe"
    else
        ext=""
    fi
    CGO_ENABLED="$cgo" GOOS="$goos" GOARCH="$goarch" \
        go build -trimpath -ldflags "$LDFLAGS" -o "$dir/ee-setup$ext" "$HERE"
    cp "$HERE/README.md" "$HERE/LICENSE" "$dir/"
    if [ "$goos" = windows ]; then
        (cd "$HERE/dist" && zip -X -qr "$name.zip" "$name")
    else
        COPYFILE_DISABLE=1 tar --no-xattrs --uid 0 --gid 0 --uname root --gname root \
            --sort=name -czf "$HERE/dist/$name.tar.gz" -C "$HERE/dist" "$name"
    fi
    rm -rf "$dir"
}

do_release() {
    echo "[*] Release $VERSION"
    rm -rf "$HERE/dist"; mkdir -p "$HERE/dist"
    # macOS Apple Silicon: cgo + libusb, full USB switch.
    package darwin arm64 1
    # macOS Intel and Linux: static builds, EE72 path only (no libusb here).
    package darwin amd64 0
    package linux amd64 0
    package windows amd64 0
    ( cd "$HERE/dist" && shasum -a 256 *.tar.gz *.zip > SHA256SUMS )
    ls -lh "$HERE/dist"
}

case "${1:-default}" in
    assets)  do_assets ;;
    build)   do_build "$2" ;;
    release) do_assets; do_release ;;
    default) do_assets; do_build ;;
    *) echo "usage: $0 {assets|build [GOOS]|release}"; exit 2 ;;
esac
