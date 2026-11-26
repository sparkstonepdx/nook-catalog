#!/bin/sh
# Pins a Subduction nightly: its tag, server version and both binaries' sha256 into fetch.sh, and VERSION to
# vMAJOR.MINOR.YYYYMMDD from the server's version and the nightly's date (v0.22.20261126), so every nightly is newer
# than the last to nook's store and a new minor server version is a minor update. Prints the new VERSION, or nothing
# when that nightly is already pinned.
#   pin.sh                 the newest nightly
#   pin.sh TAG             that one (v0.22.0-nightly.2026-11-26)
# Needs gh (any token can read the public repo), curl and sha256sum.
set -eu
cd "$(dirname "$0")"
REPO=inkandswitch/subduction
TAG="${1:-}"
if [ -z "$TAG" ]; then
  TAG=$(gh api "repos/$REPO/releases?per_page=30" -q '[.[] | select(.draft | not) | select(.tag_name | test("^v[0-9]+\\.[0-9]+\\.[0-9]+-nightly\\.[0-9]{4}-[0-9]{2}-[0-9]{2}$"))][0].tag_name')
fi
case "$TAG" in v*-nightly.*) ;; *) echo "not a nightly tag: '$TAG'" >&2; exit 1 ;; esac
if grep -q "^TAG=\"$TAG\"$" fetch.sh; then exit 0; fi
VER=${TAG#v}; VER=${VER%%-nightly.*}
DATE=$(echo "${TAG##*-nightly.}" | tr -d '-')
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
sum() { curl -fsSL -o "$tmp/$1" "https://github.com/$REPO/releases/download/$TAG/$1" && sha256sum "$tmp/$1" | cut -d' ' -f1; }
A=$(sum "subduction-linux-x86_64-$VER"); B=$(sum "subduction-linux-aarch64-$VER")
sed -i -e "s/^TAG=.*/TAG=\"$TAG\"/" -e "s/^VER=.*/VER=\"$VER\"/" -e "s/^SUM_AMD64=.*/SUM_AMD64=\"$A\"/" -e "s/^SUM_ARM64=.*/SUM_ARM64=\"$B\"/" fetch.sh
NEW="v$(echo "$VER" | cut -d. -f1-2).$DATE"
echo "$NEW" > VERSION
echo "$NEW"
