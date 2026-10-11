#!/usr/bin/env bash
# End-to-end checks for plan nntp-audit-tests (## End-to-end). Uses scratch data only.
# Usage: PORT=21119 DATA=./data-test-nntp-audit-tests scripts/test-nntp-server.sh [--build]
# Env: PORT (21119), DATA (./data-test-nntp-audit-tests), BIN (./build/pugleaf-nntp-server),
#      MGR (./build/pugleaf-nntpmgr), NNTPHOST (example.org; the server checks it by DNS lookup)
# Safety: the server and nntpmgr run with their working directory in $DATA/run and an
# absolute -data, so cwd-relative defaults (./data) can never reach the checkout's ./data.
# Checks: N01-N11 come from `go test -tags e2e -run TestE2EBinary ./cmd/nntp-server/`;
# N20-N22 are DoS probes reported as INFO (SEC-2, SEC-3, SEC-1) until nntp-server-hardening
# turns them into PASS checks; N30 is the SIGINT shutdown check.
set -u
cd "$(git rev-parse --show-toplevel)" || exit 2
PORT="${PORT:-21119}"
DATA="${DATA:-./data-test-nntp-audit-tests}"
BIN="${BIN:-./build/pugleaf-nntp-server}"
MGR="${MGR:-./build/pugleaf-nntpmgr}"
NNTPHOST="${NNTPHOST:-example.org}"
E2E_USER=e2euser0001
E2E_PASS=e2epass0001
case "$DATA" in *..*) echo "refusing DATA=$DATA (no .. allowed)"; exit 2 ;; esac
case "$DATA" in ./data-test-*) ;; *) echo "refusing DATA=$DATA (must start with ./data-test-)"; exit 2 ;; esac
case "$PORT" in ''|*[!0-9]*) echo "refusing PORT=$PORT (not a number)"; exit 2 ;; esac
for t in sqlite3 timeout nc go; do command -v "$t" >/dev/null || { echo "missing tool: $t"; exit 2; }; done
[ -e ./.update ] && { echo "refusing to run: ./.update exists in $(pwd)"; exit 2; }
# processor.SetHostname resolves -nntphostname (net.LookupIP) and log.Fatal's without DNS (OPS-1)
if command -v getent >/dev/null; then
  getent hosts "$NNTPHOST" >/dev/null 2>&1 || { echo "SKIP dns: $NNTPHOST does not resolve"; exit 2; }
fi
if [ "${1:-}" = "--build" ]; then ./build_nntp-server.sh || exit 2; ./build_nntpmgr.sh || exit 2; fi
[ -x "$BIN" ] || { echo "missing $BIN (run ./build_nntp-server.sh or pass --build)"; exit 2; }
[ -x "$MGR" ] || { echo "missing $MGR (run ./build_nntpmgr.sh or pass --build)"; exit 2; }
BIN_ABS="$(cd "$(dirname "$BIN")" && pwd -P)/$(basename "$BIN")"
MGR_ABS="$(cd "$(dirname "$MGR")" && pwd -P)/$(basename "$MGR")"

PASSES=0; FAILS=0; INFOS=0; PID=""
pass() { PASSES=$((PASSES+1)); echo "PASS $1 [$2] $3"; }
fail() { FAILS=$((FAILS+1)); echo "FAIL $1 [$2] $3 :: ${4:-}"; }
info() { INFOS=$((INFOS+1)); echo "INFO $1 [$2] $3"; }
port_open() { nc -z -w 1 127.0.0.1 "$PORT" >/dev/null 2>&1; }
alive() { [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; }
rss_kb() { awk '/^VmRSS:/{print $2}' "/proc/$PID/status" 2>/dev/null; }
cpu_ticks() { awk '{print $14+$15}' "/proc/$PID/stat" 2>/dev/null; }
start_server() {
  ( cd "$RUN" && exec "$BIN_ABS" -data "$DATA_ABS" -nntptcpport "$PORT" -nntphostname "$NNTPHOST" ) >>"$LOG" 2>&1 &
  PID=$!
  disown "$PID" 2>/dev/null || true # no job notice when a probe has to kill -9 it
  for _ in $(seq 1 90); do
    port_open && return 0
    alive || { PID=""; return 1; }
    sleep 1
  done
  return 1
}
stop_server() {
  [ -n "$PID" ] || return 0
  kill -INT "$PID" 2>/dev/null
  for _ in $(seq 1 90); do alive || { PID=""; return 0; }; sleep 1; done
  kill -KILL "$PID" 2>/dev/null; PID=""
}
setup_fail() {
  echo "$1"
  if grep -q 'DNS resolution failed' "$LOG" 2>/dev/null; then echo "SKIP dns: $NNTPHOST"; fi
  tail -40 "$LOG" 2>/dev/null
  exit 2
}
trap stop_server EXIT

# refuse to test against anything but a free port: a running server there would get the checks
port_open && { echo "refusing to run: something already answers on 127.0.0.1:$PORT"; exit 2; }

rm -rf "$DATA"; mkdir -p "$DATA/run" || exit 2
DATA_ABS=$(cd "$DATA" && pwd -P)
RUN="$DATA_ABS/run"
LOG="$DATA/server.log"; DB="$DATA/cfg/pugleaf.sq3"
q() { sqlite3 -cmd '.timeout 5000' "$DB" "$1"; }

# ---------------------------------------------------------------- setup
# 1. first start creates the schema
start_server || setup_fail "server failed to start"
stop_server
[ "$(q "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='newsgroups'" 2>/dev/null)" = 1 ] \
  || setup_fail "server did not create the schema in $DB (does $BIN honor -data?)"
# 2. seed the groups
q "INSERT INTO newsgroups(name,description,last_article,message_count,active,hierarchy,status,low_water,high_water)
   VALUES('e2e.test.a','',0,0,1,'e2e','y',1,0),('e2e.test.b','',0,0,1,'e2e','y',1,0)" \
  || setup_fail "seeding $DB failed"
# 3. the posting user (nntpmgr echoes the password into its log: scratch data only, OPS-2)
( cd "$RUN" && exec "$MGR_ABS" -data "$DATA_ABS" -create -username "$E2E_USER" -password "$E2E_PASS" -posting -maxconns 4 ) \
  >"$DATA/nntpmgr.log" 2>&1 || setup_fail "nntpmgr -create failed: $(tail -3 "$DATA/nntpmgr.log")"
# 4. start again in the background
start_server || setup_fail "server failed to restart"

# ---------------------------------------------------------------- N01-N11 (go test, build tag e2e)
GOLOG="$DATA/e2e-go-test.log"
PUGLEAF_E2E_ADDR="127.0.0.1:$PORT" PUGLEAF_E2E_USER="$E2E_USER" PUGLEAF_E2E_PASS="$E2E_PASS" PUGLEAF_E2E_GROUP=e2e.test.a \
  timeout 300 go test -tags e2e -count=1 -v -run '^TestE2EBinary$' ./cmd/nntp-server/ >"$GOLOG" 2>&1
rc=$?
lines=$(grep -E '^(PASS|FAIL|INFO) N[0-9]+ ' "$GOLOG")
if [ -n "$lines" ]; then printf '%s\n' "$lines"; fi
PASSES=$((PASSES + $(printf '%s\n' "$lines" | grep -c '^PASS ')))
FAILS=$((FAILS + $(printf '%s\n' "$lines" | grep -c '^FAIL ')))
INFOS=$((INFOS + $(printf '%s\n' "$lines" | grep -c '^INFO ')))
n=$(printf '%s\n' "$lines" | grep -cE '^(PASS|FAIL) N(0[1-9]|1[01]) ')
if [ "$n" != 11 ]; then
  fail N00 nntp "go test reported $n of 11 client steps" "rc=$rc; see $GOLOG: $(tail -5 "$GOLOG" | tr '\n' ' ')"
fi
alive || setup_fail "server died during the client steps"

# ---------------------------------------------------------------- N20 (SEC-2): LISTGROUP creates a group DB
before=$(find "$DATA/db" -name 'zz_no_such.db' 2>/dev/null | wc -l)
reply=""
if exec 3<>"/dev/tcp/127.0.0.1/$PORT"; then
  printf 'LISTGROUP zz.no.such\r\nQUIT\r\n' >&3
  reply=$(timeout 10 cat <&3 | tr -d '\r' | tr '\n' '|')
  exec 3>&- 3<&-
fi
after=$(find "$DATA/db" -name 'zz_no_such.db' 2>/dev/null | wc -l)
info N20 sec-2 "LISTGROUP zz.no.such: zz_no_such.db files before=$before after=$after reply='${reply:0:120}'"

# ---------------------------------------------------------------- N21 (SEC-3): 2 MiB command line without LF
r0=$(rss_kb)
if exec 3<>"/dev/tcp/127.0.0.1/$PORT"; then
  timeout 10 bash -c 'head -c 2097152 /dev/zero | tr "\000" A' >&3
  sleep 2
  r1=$(rss_kb)
  exec 3>&- 3<&-
else
  r1="?"
fi
sleep 1
info N21 sec-3 "2 MiB line without LF: server VmRSS before=${r0:-?}kB after=${r1:-?}kB alive=$(alive && echo yes || echo no)"

# ---------------------------------------------------------------- N30: SIGINT shutdown within 30 s
if alive; then
  kill -INT "$PID" 2>/dev/null
  t0=$(date +%s); exited=no
  for _ in $(seq 1 30); do alive || { exited=yes; break; }; sleep 1; done
  if [ "$exited" = yes ]; then
    pass N30 shutdown "SIGINT exits within 30s ($(( $(date +%s) - t0 ))s)"
    PID=""
  else
    kill -KILL "$PID" 2>/dev/null; PID=""
    fail N30 shutdown "SIGINT exits within 30s" "still running after 30s, killed; log tail: $(tail -5 "$LOG" | tr '\n' ' ')"
  fi
else
  fail N30 shutdown "SIGINT exits within 30s" "server not running before the check"
  PID=""
fi

# ---------------------------------------------------------------- N22 (SEC-1): XHDR spin, last, fresh start
if start_server; then
  if exec 3<>"/dev/tcp/127.0.0.1/$PORT"; then
    printf 'GROUP e2e.test.a\r\nXHDR subject 1-9223372036854775807\r\n' >&3
    sleep 1
    hz=$(getconf CLK_TCK 2>/dev/null || echo 100)
    c0=$(cpu_ticks); sleep 5; c1=$(cpu_ticks)
    pscpu=$(ps -o %cpu= -p "$PID" 2>/dev/null | tr -d ' ')
    cpu="?"
    [ -n "$c0" ] && [ -n "$c1" ] && cpu=$(( (c1 - c0) * 100 / (hz * 5) ))
    kill -INT "$PID" 2>/dev/null
    exited=no
    for _ in $(seq 1 10); do alive || { exited=yes; break; }; sleep 1; done
    [ "$exited" = no ] && kill -KILL "$PID" 2>/dev/null
    PID=""
    exec 3>&- 3<&-
    info N22 sec-1 "XHDR subject 1-9223372036854775807: cpu=${cpu}% over 5s (ps %cpu=${pscpu:-?}) exited_on_sigint_within_10s=$exited"
  else
    info N22 sec-1 "could not connect for the spin probe"
  fi
else
  fail N22 sec-1 "fresh start for the spin probe" "server failed to start; log tail: $(tail -5 "$LOG" | tr '\n' ' ')"
fi

echo "SUMMARY pass=$PASSES fail=$FAILS info=$INFOS data=$DATA log=$LOG"
[ "$FAILS" -gt 125 ] && FAILS=125
exit "$FAILS"
