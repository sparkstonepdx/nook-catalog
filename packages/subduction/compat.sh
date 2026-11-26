#!/bin/bash
# Checks a new Subduction server before it's pinned: data the old server wrote, and the clients the apps use.
#   compat.sh OLD_BINARY NEW_BINARY
# 1. The old server, on a fresh data folder: a 2.6 client (what the apps run today) and a 3.0 client (what they're
#    moving to) each make a doc, and each edits the other's.
# 2. The new server, on that same folder and key: both clients read both docs with every edit, edit them again,
#    and read each other's new edits.
# Exits non-zero on the first thing that doesn't match. Needs node; installs the clients in compat/v2 and compat/v3.
set -euo pipefail
OLD="$1"; NEW="$2"
chmod +x "$OLD" "$NEW"
here="$(cd "$(dirname "$0")" && pwd)"
for v in v2 v3; do [ -d "$here/compat/$v/node_modules" ] || (cd "$here/compat/$v" && npm ci --no-audit --no-fund --silent); done
work=$(mktemp -d); pid=""
stop() { [ -n "$pid" ] && kill "$pid" 2>/dev/null && wait "$pid" 2>/dev/null || true; pid=""; }
trap 'stop; rm -rf "$work"' EXIT
head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$work/key"
PORT=18090; WS="ws://127.0.0.1:$PORT"
start() {
  "$1" server --socket "127.0.0.1:$PORT" --data-dir "$work/data" --key-file "$work/key" --service-name "127.0.0.1:$PORT" >>"$work/server.log" 2>&1 &
  pid=$!
  for _ in $(seq 50); do (echo >/dev/tcp/127.0.0.1/$PORT) 2>/dev/null && return 0; sleep 0.2; done
  echo "$1 didn't start:"; tail -20 "$work/server.log"; exit 1
}
c() { node "$here/compat/$1/client.mjs" "$2" "$WS" ${3:+"$3"} ${4:+"$4"} ${5:+"$5"}; }
expect() { # client doc want: the doc read by that client must hold every key=value in want
  got=$(c "$1" read "$2")
  for kv in $3; do echo "$got" | grep -q "\"${kv%%=*}\":\"${kv#*=}\"" || { echo "FAIL: $1 read $2 as $got, missing ${kv}"; tail -20 "$work/server.log"; exit 1; }; done
  echo "ok: $1 reads $2 with $3"
}

echo "== old server: $("$OLD" --version)"
start "$OLD"
d2=$(c v2 make); d3=$(c v3 make)
c v3 set "$d2" old_v3 yes >/dev/null; c v2 set "$d3" old_v2 yes >/dev/null
expect v2 "$d2" "old_v3=yes"; expect v3 "$d3" "old_v2=yes"
stop

echo "== new server: $("$NEW" --version), same data and key"
start "$NEW"
expect v2 "$d2" "old_v3=yes"; expect v3 "$d2" "old_v3=yes"
expect v2 "$d3" "old_v2=yes"; expect v3 "$d3" "old_v2=yes"
c v2 set "$d3" new_v2 yes >/dev/null; c v3 set "$d2" new_v3 yes >/dev/null
expect v3 "$d3" "old_v2=yes new_v2=yes"; expect v2 "$d2" "old_v3=yes new_v3=yes"
echo "== compatible"
