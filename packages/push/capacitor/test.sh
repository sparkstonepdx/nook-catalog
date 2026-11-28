#!/bin/sh
# Decrypts the push server's test vectors with the Android code (plain Java). The packages workflow runs it.
set -eu
cd "$(dirname "$0")"
out=$(mktemp -d); trap 'rm -rf "$out"' EXIT
javac -d "$out" android/src/main/java/com/sparkstonepdx/push/PushCrypto.java android/src/test/java/com/sparkstonepdx/push/PushCryptoTest.java
java -cp "$out" com.sparkstonepdx.push.PushCryptoTest testdata/vectors.json
