#!/bin/sh
# Builds the release assets required by the CLIProxyAPI Plugin Store for every
# supported platform: darwin/amd64, darwin/arm64, linux/amd64, linux/arm64 and
# windows/amd64.
#
# darwin is built natively with the local clang (needs macOS). linux and windows
# are built in Docker (golang:1.26-bookworm) with the aarch64 and mingw-w64 cross
# compilers. Like build.sh, it needs the CLIProxyAPI source in ../CLIProxyAPI
# (see the replace directive in go.mod).
#
# Output: out/release/key-router_<version>_<goos>_<goarch>.zip and out/release/checksums.txt
set -e
REPO=$(cd "$(dirname "$0")" && pwd)
PARENT=$(dirname "$REPO")
NAME=$(basename "$REPO")
ID=key-router
CPA_VERSION=${CPA_VERSION:-v8.0.17}
VERSION=$(sed -n 's/^const pluginVersion = "\(.*\)"/\1/p' "$REPO/main.go")
[ -n "$VERSION" ] || { echo "pluginVersion not found in main.go" >&2; exit 1; }

STAGE="$REPO/out/stage"
RELEASE="$REPO/out/release"
rm -rf "$STAGE" "$RELEASE"
mkdir -p "$STAGE" "$RELEASE"

if [ ! -d "$PARENT/CLIProxyAPI" ]; then
  echo "CLIProxyAPI source missing, cloning $CPA_VERSION into $PARENT/CLIProxyAPI" >&2
  git clone -q --depth 1 --branch "$CPA_VERSION" https://github.com/router-for-me/CLIProxyAPI "$PARENT/CLIProxyAPI"
fi

# darwin: native build on macOS, one library per architecture
for arch in amd64 arm64; do
  dir="$STAGE/darwin_$arch"
  mkdir -p "$dir"
  (cd "$REPO" && CGO_ENABLED=1 GOOS=darwin GOARCH=$arch go build -buildmode=c-shared -trimpath -ldflags="-s -w" -o "$dir/$ID.dylib" .)
  rm -f "$dir/$ID.h"
done

# linux and windows: cross build in Docker. The container writes into out/stage,
# the host packs the zips below.
DOCKER_CMD='set -e
apt-get update -qq >/dev/null
apt-get install -y -qq gcc-x86-64-linux-gnu gcc-aarch64-linux-gnu gcc-mingw-w64-x86-64 >/dev/null
go mod download
build() { # goos goarch cc ext
  dir="out/stage/$1_$2"
  mkdir -p "$dir"
  CGO_ENABLED=1 GOOS=$1 GOARCH=$2 CC=$3 go build -buildmode=c-shared -trimpath -ldflags="-s -w" -o "$dir/'"$ID"'.$4" .
  rm -f "$dir/'"$ID"'.h"
}
build linux amd64 x86_64-linux-gnu-gcc so
build linux arm64 aarch64-linux-gnu-gcc so
build windows amd64 x86_64-w64-mingw32-gcc dll'

docker run --rm \
  -v "$PARENT:/work" \
  -v key-router-gomod:/go/pkg/mod -v key-router-gocache:/root/.cache/go-build \
  -w "/work/$NAME" golang:1.26-bookworm sh -c "$DOCKER_CMD"

# Pack each platform: the dynamic library sits at the zip root, named <id>.<ext>
for dir in "$STAGE"/*; do
  target=$(basename "$dir")
  goos=${target%_*}
  goarch=${target#*_}
  case "$goos" in
    darwin) lib="$ID.dylib" ;;
    linux) lib="$ID.so" ;;
    windows) lib="$ID.dll" ;;
  esac
  zip -q -j -X "$RELEASE/${ID}_${VERSION}_${goos}_${goarch}.zip" "$dir/$lib"
done

(cd "$RELEASE" && shasum -a 256 ./*.zip | sed 's|  \./|  |' > checksums.txt)
cat "$RELEASE/checksums.txt"
