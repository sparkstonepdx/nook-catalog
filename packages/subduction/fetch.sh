#!/bin/sh
# Puts the pinned Subduction server for one architecture (amd64 or arm64) in bin/, for the release build to embed.
# Run by the packages workflow before `go build`. pin.sh changes the four values below (the "subduction nightly"
# workflow runs it).
set -eu
TAG="v0.22.0-nightly.2026-11-25"
VER="0.22.0"
SUM_AMD64="34701cf84fcb92c85672505f89834b46d687b2b67476b2151db1e9f41639c2d2"
SUM_ARM64="743c75d2455fe2606e145a1babe37596eba04e7f2c42c8a88caedc225073c9ba"
case "$1" in
  amd64) asset="subduction-linux-x86_64-$VER"; sum="$SUM_AMD64" ;;
  arm64) asset="subduction-linux-aarch64-$VER"; sum="$SUM_ARM64" ;;
  *) echo "usage: fetch.sh amd64|arm64" >&2; exit 1 ;;
esac
cd "$(dirname "$0")"
curl -fsSL -o bin/subduction_cli.tmp "https://github.com/inkandswitch/subduction/releases/download/$TAG/$asset"
echo "$sum  bin/subduction_cli.tmp" | sha256sum -c - >/dev/null || { echo "$asset doesn't match its pinned sha256" >&2; rm -f bin/subduction_cli.tmp; exit 1; }
mv bin/subduction_cli.tmp bin/subduction_cli
echo "$TAG" > bin/TAG
