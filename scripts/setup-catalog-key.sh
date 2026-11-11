#!/bin/sh
# One-time: make this catalog's signing key. The private half goes straight into the repo's
# CATALOG_SIGNING_KEY secret (never written to disk); the public half is written to catalog.pub for you
# to commit. Run from the repo root, with gh logged in.
set -eu
[ -d apps ] || { echo "run from the catalog repo's root"; exit 1; }
if [ -s catalog.pub ]; then
  echo "catalog.pub already has a key. Servers that added this catalog pinned it, so a new key makes"
  echo "them refuse the catalog until they remove and add it again. Delete catalog.pub first if you mean it."
  exit 1
fi
keys=$(go run github.com/sparkstonepdx/nook/cmd/release-sign@latest keygen)
printf '%s\n' "$keys" | sed -n 's/^PRIVATE=//p' | gh secret set CATALOG_SIGNING_KEY
printf '%s\n' "$keys" | sed -n 's/^PUBLIC=//p' > catalog.pub
echo "Done. Commit catalog.pub and push; the catalog workflow publishes the dist branch."
