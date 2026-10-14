#!/usr/bin/env bash
# End-to-end checks for plan nntp-client-transfer. Scratch data and 127.0.0.1 only.
# Usage: scripts/test-nntp-client-transfer.sh [--build]
# Env: PORT (21219 nntp-server), PEERPORT (21220 fake peer), WEBPORT (21221), PPROFPORT (21222),
#      SILENTPORT (21223 silent peer), HRPORT (21224), DATA (./data-test-nntp-client-transfer),
#      NNTPHOST (example.org; nntp-server and nntp-transfer check it by DNS lookup)
set -u
cd "$(git rev-parse --show-toplevel)" || exit 2
PORT="${PORT:-21219}"; PEERPORT="${PEERPORT:-21220}"; WEBPORT="${WEBPORT:-21221}"; PPROFPORT="${PPROFPORT:-21222}"
SILENTPORT="${SILENTPORT:-21223}"; HRPORT="${HRPORT:-21224}"
DATA="${DATA:-./data-test-nntp-client-transfer}"; NNTPHOST="${NNTPHOST:-example.org}"
SRV=./build/pugleaf-nntp-server; MGR=./build/pugleaf-nntpmgr; TX=./build/nntp-transfer; HR=./build/history-rebuild
E2E_USER=ctuser00001; E2E_PASS=ctpass00001; PEER_USER=peeruser01; PEER_PASS=peer-secret-0123
case "$DATA" in *..*) echo "refusing DATA=$DATA (no .. allowed)"; exit 2 ;; esac
case "$DATA" in ./data-test-*) ;; *) echo "refusing DATA=$DATA (must start with ./data-test-)"; exit 2 ;; esac
for p in "$PORT" "$PEERPORT" "$WEBPORT" "$PPROFPORT" "$SILENTPORT" "$HRPORT"; do
  case "$p" in ''|*[!0-9]*) echo "refusing port '$p'"; exit 2 ;; esac
  nc -z -w 1 127.0.0.1 "$p" >/dev/null 2>&1 && { echo "refusing to run: 127.0.0.1:$p is in use"; exit 2; }
done
for t in sqlite3 timeout nc go curl; do command -v "$t" >/dev/null || { echo "missing tool: $t"; exit 2; }; done
[ -e ./.update ] && { echo "refusing to run: ./.update exists"; exit 2; }
if command -v getent >/dev/null; then getent hosts "$NNTPHOST" >/dev/null 2>&1 || { echo "SKIP dns: $NNTPHOST"; exit 2; }; fi
if [ "${1:-}" = "--build" ]; then
  ./build_nntp-server.sh && ./build_nntpmgr.sh && ./build_nntp-transfer.sh && ./build_history-rebuild.sh || exit 2
fi
for b in "$SRV" "$MGR" "$TX" "$HR"; do [ -x "$b" ] || { echo "missing $b (pass --build)"; exit 2; }; done
abs() { echo "$(cd "$(dirname "$1")" && pwd -P)/$(basename "$1")"; }
SRV_ABS=$(abs "$SRV"); MGR_ABS=$(abs "$MGR"); TX_ABS=$(abs "$TX"); HR_ABS=$(abs "$HR")

PASSES=0; FAILS=0; INFOS=0; SPID=""; PPID_PEER=""; PPID_SILENT=""; TXPID=""
pass() { PASSES=$((PASSES+1)); echo "PASS $1 [$2] $3"; }
fail() { FAILS=$((FAILS+1)); echo "FAIL $1 [$2] $3 :: ${4:-}"; }
info() { INFOS=$((INFOS+1)); echo "INFO $1 [$2] $3"; }
alive() { [ -n "$1" ] && kill -0 "$1" 2>/dev/null; }
stop_pid() { [ -n "$1" ] || return 0; kill -INT "$1" 2>/dev/null
  for _ in $(seq 1 60); do alive "$1" || return 0; sleep 1; done; kill -KILL "$1" 2>/dev/null; }
cleanup() { [ -d "${PEERDIR:-/nonexistent}" ] && touch "$PEERDIR/STOP" "$SILENTDIR/STOP" 2>/dev/null
  stop_pid "$TXPID"; stop_pid "$SPID"; sleep 1; stop_pid "$PPID_PEER"; stop_pid "$PPID_SILENT"; }
trap cleanup EXIT

rm -rf "$DATA"; mkdir -p "$DATA/run" "$DATA/peer" "$DATA/silent" || exit 2
DATA_ABS=$(cd "$DATA" && pwd -P); RUN="$DATA_ABS/run"; PEERDIR="$DATA_ABS/peer"; SILENTDIR="$DATA_ABS/silent"
DB="$DATA/cfg/pugleaf.sq3"; q() { sqlite3 -cmd '.timeout 5000' "$DB" "$1"; }
setup_fail() { echo "$1"; tail -30 "$DATA/server.log" 2>/dev/null; exit 2; }
start_srv() {
  ( cd "$RUN" && exec "$SRV_ABS" -data "$DATA_ABS" -nntptcpport "$PORT" -nntphostname "$NNTPHOST" ) >>"$DATA/server.log" 2>&1 &
  SPID=$!
  for _ in $(seq 1 90); do nc -z -w 1 127.0.0.1 "$PORT" >/dev/null 2>&1 && return 0; alive "$SPID" || return 1; sleep 1; done; return 1
}

# ---- setup: schema, groups, user, three posted articles (nntp-server on scratch data)
start_srv || setup_fail "nntp-server failed to start"; stop_pid "$SPID"; SPID=""
q "INSERT INTO newsgroups(name,description,last_article,message_count,active,hierarchy,status,low_water,high_water)
   VALUES('e2e.ct.a','',0,0,1,'e2e','y',1,0),('e2e.ct.b','',0,0,1,'e2e','y',1,0)" || setup_fail "seed groups"
[ "$(q "SELECT value FROM config WHERE key='local_nntp_hostname'")" = "$NNTPHOST" ] \
  || q "INSERT OR REPLACE INTO config(key,value) VALUES('local_nntp_hostname','$NNTPHOST')" || setup_fail "seed hostname"
( cd "$RUN" && exec "$MGR_ABS" -data "$DATA_ABS" -create -username "$E2E_USER" -password "$E2E_PASS" -posting -maxconns 4 ) \
  >"$DATA/nntpmgr.log" 2>&1 || setup_fail "nntpmgr -create failed"
start_srv || setup_fail "nntp-server failed to restart"
PUGLEAF_E2E_ADDR="127.0.0.1:$PORT" PUGLEAF_E2E_USER="$E2E_USER" PUGLEAF_E2E_PASS="$E2E_PASS" \
  timeout 120 go test -tags e2e -count=1 -v -run '^TestCliE2ESeed$' ./internal/nntp/ >"$DATA/seed.log" 2>&1
[ "$(grep -c '^PASS S0' "$DATA/seed.log")" = 3 ] || setup_fail "seeding POSTs failed: $(grep -E '^(PASS|FAIL) S0' "$DATA/seed.log" | tr '\n' ' ')"
for _ in $(seq 1 30); do [ "$(q "SELECT message_count FROM newsgroups WHERE name='e2e.ct.a'")" -ge 3 ] 2>/dev/null && break; sleep 1; done
stop_pid "$SPID"; SPID=""   # the transfer reads the DB directly; the server must not write concurrently
GDB=$(find "$DATA_ABS/db" -name 'e2e_ct_a.db' | head -1); [ -n "$GDB" ] || setup_fail "group DB e2e_ct_a.db not found"

# ---- fake peers (go test, build tag e2e)
start_peer() { # $1 mode, $2 port, $3 dir
  PUGLEAF_E2E_PEER_ADDR="127.0.0.1:$2" PUGLEAF_E2E_PEER_DIR="$3" PUGLEAF_E2E_PEER_MODE="$1" \
  PUGLEAF_E2E_PEER_USER="$PEER_USER" PUGLEAF_E2E_PEER_PASS="$PEER_PASS" \
  PUGLEAF_E2E_PEER_431='<ct-2@e2e.invalid>' PUGLEAF_E2E_PEER_STALL=15s \
    go test -tags e2e -count=1 -timeout 15m -run '^TestCliE2EFakePeer$' ./internal/nntp/ >"$3/peer.log" 2>&1 &
  echo $!
}
PPID_PEER=$(start_peer peer "$PEERPORT" "$PEERDIR"); PPID_SILENT=$(start_peer silent "$SILENTPORT" "$SILENTDIR")
for _ in $(seq 1 120); do [ -e "$PEERDIR/ready" ] && [ -e "$SILENTDIR/ready" ] && break; sleep 1; done
[ -e "$PEERDIR/ready" ] && [ -e "$SILENTDIR/ready" ] || setup_fail "fake peers did not start: $(tail -5 "$PEERDIR/peer.log")"

TXARGS="-data $DATA_ABS -host 127.0.0.1 -port $PEERPORT -username $PEER_USER -password $PEER_PASS -group e2e.ct.a -redis-cache=false -max-threads 1 -batch-check 10 -timeout 5"
run_tx() { ( cd "$RUN" && exec timeout 180 "$TX_ABS" $TXARGS "${@:2}" ) >"$DATA/transfer-$1.log" 2>&1; }

# ---- run 1 (background: web/pprof checks happen while the peer stalls MODE STREAM for 15 s; the poll below
#      gives up after 10 s, so all four checks run inside the stall window)
( cd "$RUN" && exec timeout 180 "$TX_ABS" $TXARGS -web-port "$WEBPORT" -pprof-port "$PPROFPORT" ) >"$DATA/transfer-1.log" 2>&1 &
TXPID=$!
for _ in $(seq 1 100); do curl -fs -o /dev/null "http://127.0.0.1:$WEBPORT/" && break; sleep 0.1; done
c=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$WEBPORT/")
[ "$c" = 200 ] && pass E09 cli-transfer "UI answers on loopback" || fail E09 cli-transfer "UI answers on loopback" "status=$c"
b=$(curl -s -w '|%{http_code}' "http://127.0.0.1:$WEBPORT/debug/pprof/cmdline")
case "$b" in *"$PEER_PASS"*) fail E10 cli-transfer "no pprof on the UI port" "password leaked" ;;
  *'|404') pass E10 cli-transfer "no pprof on the UI port" ;; *) fail E10 cli-transfer "no pprof on the UI port" "got ${b##*|}" ;; esac
c=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PPROFPORT/debug/pprof/")
b=$(curl -s -w '|%{http_code}' "http://127.0.0.1:$PPROFPORT/debug/pprof/cmdline")
case "$b" in *"$PEER_PASS"*) fail E11 cli-transfer "pprof without cmdline" "password leaked" ;;
  *'|404') [ "$c" = 200 ] && pass E11 cli-transfer "pprof without cmdline" || fail E11 cli-transfer "pprof without cmdline" "index=$c" ;;
  *) fail E11 cli-transfer "pprof without cmdline" "cmdline=${b##*|} index=$c" ;; esac
if command -v ss >/dev/null; then
  l=$(ss -ltnH "sport = :$WEBPORT" 2>/dev/null | awk '{print $4}' | tr '\n' ' ')
  case "$l" in 127.0.0.1:"$WEBPORT"" ") pass E12 cli-transfer "UI bound to 127.0.0.1" ;; *) fail E12 cli-transfer "UI bound to 127.0.0.1" "listen='$l'" ;; esac
else info E12 cli-transfer "ss not installed"; fi
wait "$TXPID"; rc=$?; TXPID=""
if [ "$rc" = 0 ] && grep -q '^"AUTHINFO PASS' "$PEERDIR/commands.log" && grep -q '^"MODE STREAM' "$PEERDIR/commands.log" \
   && grep -q '^"CHECK ' "$PEERDIR/commands.log" && grep -q '^"TAKETHIS ' "$PEERDIR/commands.log"; then
  pass E01 cli-conn "authenticated streaming transfer completes"
else fail E01 cli-conn "authenticated streaming transfer completes" "rc=$rc; $(tail -3 "$DATA/transfer-1.log" | tr '\n' ' ')"; fi
r=$(sqlite3 "$DATA/transfer-progress/transfer-progress.db" "SELECT retry FROM transfers WHERE newsgroup='e2e.ct.a' ORDER BY id DESC LIMIT 1" 2>/dev/null)
[ "$r" = 1 ] && pass E06 cli-tools "431 recorded as retry" || fail E06 cli-tools "431 recorded as retry" "retry='$r'"
PUGLEAF_E2E_PEER_DIR="$PEERDIR" PUGLEAF_E2E_GROUPDB="$GDB" PUGLEAF_E2E_LOCALHOST="$NNTPHOST" PUGLEAF_E2E_PHASE=1 \
  timeout 120 go test -tags e2e -count=1 -v -run '^TestCliE2EVerify$' ./internal/nntp/ >"$DATA/verify-1.log" 2>&1
lines=$(grep -E '^(PASS|FAIL|INFO) E[0-9]+b? ' "$DATA/verify-1.log"); [ -n "$lines" ] && printf '%s\n' "$lines"
PASSES=$((PASSES + $(printf '%s\n' "$lines" | grep -c '^PASS '))); FAILS=$((FAILS + $(printf '%s\n' "$lines" | grep -c '^FAIL ')))
[ "$(printf '%s\n' "$lines" | grep -cE '^(PASS|FAIL) E(03|04|05|15) ')" = 4 ] || fail E00 any "verify phase 1 incomplete" "$(tail -3 "$DATA/verify-1.log" | tr '\n' ' ')"

# ---- run 2 (re-offers the group) and run 3 (skips it)
run_tx 2
if ! grep -q 'Skipping newsgroup e2e.ct.a' "$DATA/transfer-2.log"; then pass E07 cli-tools "incomplete group re-offered"
else fail E07 cli-tools "incomplete group re-offered" "run 2 skipped it"; fi
PUGLEAF_E2E_PEER_DIR="$PEERDIR" PUGLEAF_E2E_GROUPDB="$GDB" PUGLEAF_E2E_LOCALHOST="$NNTPHOST" PUGLEAF_E2E_PHASE=2 \
  timeout 120 go test -tags e2e -count=1 -v -run '^TestCliE2EVerify$' ./internal/nntp/ >"$DATA/verify-2.log" 2>&1
lines=$(grep -E '^(PASS|FAIL) E07b ' "$DATA/verify-2.log"); [ -n "$lines" ] && printf '%s\n' "$lines"
PASSES=$((PASSES + $(printf '%s\n' "$lines" | grep -c '^PASS '))); FAILS=$((FAILS + $(printf '%s\n' "$lines" | grep -c '^FAIL ')))
[ -n "$lines" ] || fail E07b cli-tools "verify phase 2 did not report" "$(tail -3 "$DATA/verify-2.log" | tr '\n' ' ')"
run_tx 3
grep -q 'Skipping newsgroup e2e.ct.a - already has transfer results' "$DATA/transfer-3.log" \
  && pass E08 cli-tools "complete group skipped" || fail E08 cli-tools "complete group skipped" "$(tail -2 "$DATA/transfer-3.log" | tr '\n' ' ')"

# ---- E16: SIGTERM takes the graceful path. Target the silent peer so the process stays busy (connects time out
#      after -timeout 2 and are retried) until it is signalled.
( cd "$RUN" && exec timeout 120 "$TX_ABS" -data "$DATA_ABS" -host 127.0.0.1 -port "$SILENTPORT" -group e2e.ct.a \
    -redis-cache=false -use-progress-db=false -max-threads 1 -timeout 2 ) >"$DATA/transfer-term.log" 2>&1 &
TXPID=$!   # the pid of GNU timeout, which forwards SIGTERM to nntp-transfer
for _ in $(seq 1 100); do grep -q 'Found [0-9]* newsgroups to transfer' "$DATA/transfer-term.log" && break; alive "$TXPID" || break; sleep 0.1; done
sleep 2
kill -TERM "$TXPID" 2>/dev/null
t0=$(date +%s); exited=no
for _ in $(seq 1 30); do alive "$TXPID" || { exited=yes; break; }; sleep 1; done
[ "$exited" = yes ] || kill -KILL "$TXPID" 2>/dev/null
wait "$TXPID" 2>/dev/null; TXPID=""
if grep -q 'Received shutdown signal' "$DATA/transfer-term.log"; then
  pass E16 cli-transfer "SIGTERM handled like SIGINT (exited=$exited after $(( $(date +%s) - t0 ))s)"
  [ "$exited" = yes ] || info E16b cli-transfer "graceful shutdown did not finish within 30s (same path as SIGINT; not this plan's finding)"
else fail E16 cli-transfer "SIGTERM handled like SIGINT" "no 'Received shutdown signal' in log; $(tail -2 "$DATA/transfer-term.log" | tr '\n' ' ')"; fi

# ---- E02 silent peer, E13 history-rebuild pprof, E14 logs
t0=$(date +%s)
( cd "$RUN" && exec timeout 30 "$TX_ABS" -test-conn -host 127.0.0.1 -port "$SILENTPORT" -timeout 2 -group x -data "$DATA_ABS" ) >"$DATA/testconn.log" 2>&1
rc=$?; el=$(( $(date +%s) - t0 ))
[ "$rc" != 0 ] && [ "$rc" != 124 ] && [ "$el" -le 15 ] && pass E02 cli-conn "silent upstream fails fast (${el}s)" \
  || fail E02 cli-conn "silent upstream fails fast" "rc=$rc elapsed=${el}s"
( cd "$RUN" && exec timeout 20 "$HR_ABS" -data "$DATA_ABS" -analyze-only -pprof "0.0.0.0:$HRPORT" ) >"$DATA/hr-pprof.log" 2>&1
rc=$?
[ "$rc" != 0 ] && [ "$rc" != 124 ] && grep -qi loopback "$DATA/hr-pprof.log" && pass E13 cli-transfer "history-rebuild refuses non-loopback -pprof" \
  || fail E13 cli-transfer "history-rebuild refuses non-loopback -pprof" "rc=$rc $(tail -2 "$DATA/hr-pprof.log" | tr '\n' ' ')"
if grep -l 'WARNING: DATA RACE\|^panic:' "$DATA"/transfer-*.log >/dev/null 2>&1; then
  fail E14 any "no race or panic in transfer logs" "$(grep -l 'WARNING: DATA RACE\|^panic:' "$DATA"/transfer-*.log | tr '\n' ' ')"
else pass E14 any "no race or panic in transfer logs"; fi

echo "SUMMARY pass=$PASSES fail=$FAILS info=$INFOS data=$DATA"
[ "$FAILS" -gt 125 ] && FAILS=125
exit "$FAILS"
