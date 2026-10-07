#!/bin/sh
# Builds key-router.so for linux/amd64 in Docker (golang:1.26-bookworm, the same
# toolchain and libc as the official CLIProxyAPI image).
#
# The plugin compiles against the CLIProxyAPI source in ../CLIProxyAPI (see the
# replace directive in go.mod). When the directory is missing, the tag given by
# CPA_VERSION is cloned there; keep it equal to the deployed CLIProxyAPI version.
#
# Usage: ./build.sh [test]        Output: out/key-router.so
set -e
REPO=$(cd "$(dirname "$0")" && pwd)
PARENT=$(dirname "$REPO")
NAME=$(basename "$REPO")
CPA_VERSION=${CPA_VERSION:-v8.0.17}

CMD='go mod tidy && go build -buildmode=c-shared -trimpath -ldflags="-s -w" -o out/key-router.so . && rm -f out/key-router.h'
[ "$1" = "test" ] && CMD="go mod tidy && go vet ./... && go test -count=1 ./... && $CMD"
if [ ! -d "$PARENT/CLIProxyAPI" ]; then
  CMD="git clone -q --depth 1 --branch $CPA_VERSION https://github.com/router-for-me/CLIProxyAPI /work/CLIProxyAPI && $CMD"
fi

mkdir -p "$REPO/out"
docker run --rm \
  -v "$PARENT:/work" \
  -v key-router-gomod:/go/pkg/mod -v key-router-gocache:/root/.cache/go-build \
  -w "/work/$NAME" golang:1.26-bookworm sh -c "$CMD"
ls -l "$REPO/out/key-router.so"
