#!/usr/bin/env bash
# End-to-end checks for plan nntp-audit-tests (## End-to-end), nntp-server-hardening,
# nntp-intake-injection and nntp-rfc-commands. Scratch data only.
# Usage: PORT=21119 DATA=./data-test-nntp-server-hardening scripts/test-nntp-server.sh [--build]
# Env: PORT (21119), DATA (./data-test-nntp-audit-tests), BIN (./build/pugleaf-nntp-server),
#      MGR (./build/pugleaf-nntpmgr), NNTPHOST (example.org; the server checks it by DNS lookup)
# Safety: the server and nntpmgr run with their working directory in $DATA/run and an
# absolute -data, so cwd-relative defaults (./data) can never reach the checkout's ./data.
# Checks: N01-N11 come from `go test -tags e2e -run TestE2EBinary ./cmd/nntp-server/`;
# N40 and N42-N49 (plan nntp-intake-injection, tag nntp) from `go test -tags e2e -run TestE2EBinaryIntake`,
# N41 [nntp-intake] is a shell check (no group row or file for an unknown group);
# N20-N28 are PASS/FAIL checks of plan nntp-server-hardening (tags nntp-session, nntp-overview,
# nntp-read, nntp-auth-db); N30 is the SIGINT shutdown check (with an idle client connected).
# N50-N57 (nntp-rfc-commands, tag rfc) come from `go test -tags e2e -run TestE2EBinaryRFC`; N58-N59 are spare.
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
  ( cd "$RUN" && exec "$BIN_ABS" -data "$DATA_ABS" -nntptcpport "$PORT" -nntphostname "$NNTPHOST" ${TLSFLAGS[@]+"${TLSFLAGS[@]}"} ) >>"$LOG" 2>&1 &
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
# N57 (nntp-rfc-commands): a scratch certificate enables STARTTLS on the plain port (no TLS port)
TLSFLAGS=(); E2E_STARTTLS=0
if ! command -v openssl >/dev/null; then
  info N57 rfc "openssl missing: STARTTLS checked without a certificate"
elif openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 2 -subj /CN=localhost \
     -keyout "$DATA_ABS/run/tls.key" -out "$DATA_ABS/run/tls.crt" >/dev/null 2>&1; then
  TLSFLAGS=(-nntpcertfile "$DATA_ABS/run/tls.crt" -nntpkeyfile "$DATA_ABS/run/tls.key"); E2E_STARTTLS=1
else
  info N57 rfc "openssl could not make a certificate: STARTTLS checked without one"
fi
q() { sqlite3 -cmd '.timeout 5000' "$DB" "$1"; }
# one NNTP client on fd 3: nn_open reads the greeting into NN_GREETING, nn_cmd sends one
# command and reads one status line into NN_LINE, nn_block reads a data block into
# NN_BLOCK as "|line|line|" (without the terminating "."), nn_close closes fd 3.
nn_open() { { exec 3<>"/dev/tcp/127.0.0.1/$PORT"; } 2>/dev/null || return 1
  IFS= read -r -t 5 NN_GREETING <&3 || { nn_close; return 1; }; NN_GREETING=${NN_GREETING%$'\r'}; }
nn_cmd() { printf '%s\r\n' "$1" >&3 2>/dev/null || return 1; NN_LINE=""
  IFS= read -r -t "${2:-5}" NN_LINE <&3 || return 1; NN_LINE=${NN_LINE%$'\r'}; }
nn_block() { NN_BLOCK="|"; local l; while IFS= read -r -t "${1:-5}" l <&3; do l=${l%$'\r'}
  [ "$l" = . ] && return 0; NN_BLOCK="$NN_BLOCK$l|"; done; return 1; }
nn_close() { { exec 3>&- 3<&-; } 2>/dev/null; }
ms_now() { date +%s%3N; }

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
# N40-N49: a group for generated headers and a moderated one
q "INSERT INTO newsgroups(name,description,last_article,message_count,active,hierarchy,status,low_water,high_water)
   VALUES('e2e.intake.gen','',0,0,1,'e2e','y',1,0),('e2e.intake.mod','',0,0,1,'e2e','m',1,0)" \
  || setup_fail "seeding the intake groups in $DB failed"
# 3. the posting user (nntpmgr no longer echoes a given password: N28 checks it; scratch data only)
( cd "$RUN" && exec "$MGR_ABS" -data "$DATA_ABS" -create -username "$E2E_USER" -password "$E2E_PASS" -posting -maxconns 4 ) \
  >"$DATA/nntpmgr.log" 2>&1 || setup_fail "nntpmgr -create failed: $(tail -3 "$DATA/nntpmgr.log")"

# ---------------------------------------------------------------- N28 [nntp-auth-db] (OPS-2): no secrets from nntpmgr
lst=$( (cd "$RUN" && exec "$MGR_ABS" -data "$DATA_ABS" -list) 2>&1 )
if printf %s "$lst" | grep -q "$E2E_USER" && ! printf %s "$lst" | grep -qE '\$2[aby]\$' \
   && ! grep -qF "$E2E_PASS" "$DATA/nntpmgr.log"; then pass N28 nntp-auth-db "nntpmgr prints no hash or given password"
else fail N28 nntp-auth-db "nntpmgr prints no hash or given password" "list/create output leaks secrets"; fi

# 4. start again in the background
start_server || setup_fail "server failed to restart"

# ---------------------------------------------------------------- N01-N11 (go test, build tag e2e)
GOLOG="$DATA/e2e-go-test.log"
PUGLEAF_E2E_ADDR="127.0.0.1:$PORT" PUGLEAF_E2E_USER="$E2E_USER" PUGLEAF_E2E_PASS="$E2E_PASS" PUGLEAF_E2E_GROUP=e2e.test.a \
  timeout 900 go test -tags e2e -count=1 -v -run '^TestE2EBinary$' ./cmd/nntp-server/ >"$GOLOG" 2>&1
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

# ---------------------------------------------------------------- N40-N49 (nntp-intake-injection)
GOLOG40="$DATA/e2e-go-test-intake.log"
PUGLEAF_E2E_ADDR="127.0.0.1:$PORT" PUGLEAF_E2E_USER="$E2E_USER" PUGLEAF_E2E_PASS="$E2E_PASS" PUGLEAF_E2E_HOST="$NNTPHOST" \
  timeout 900 go test -tags e2e -count=1 -v -run '^TestE2EBinaryIntake$' ./cmd/nntp-server/ >"$GOLOG40" 2>&1
rc=$?
lines=$(grep -E '^(PASS|FAIL|INFO) N[0-9]+ ' "$GOLOG40")
if [ -n "$lines" ]; then printf '%s\n' "$lines"; fi
PASSES=$((PASSES + $(printf '%s\n' "$lines" | grep -c '^PASS ')))
FAILS=$((FAILS + $(printf '%s\n' "$lines" | grep -c '^FAIL ')))
INFOS=$((INFOS + $(printf '%s\n' "$lines" | grep -c '^INFO ')))
n=$(printf '%s\n' "$lines" | grep -cE '^(PASS|FAIL) N(40|4[2-9]) ')
if [ "$n" != 9 ]; then
  fail N00 nntp "go test reported $n of 9 intake checks" "rc=$rc; see $GOLOG40: $(tail -5 "$GOLOG40" | tr '\n' ' ')"
fi
alive || setup_fail "server died during the intake checks"
# N41 [nntp-intake] (SEC-2): after N40 there is no file or row for the unknown group and no NULL hierarchy
files=$(find "$DATA/db" -name 'e2e_nosuch_group.db' 2>/dev/null | wc -l)
rows=$(q "SELECT count(*) FROM newsgroups WHERE name='e2e.nosuch.group'" 2>/dev/null)
nulls=$(q "SELECT count(*) FROM newsgroups WHERE hierarchy IS NULL" 2>/dev/null)
if [ "$files" = 0 ] && [ "$rows" = 0 ] && [ "$nulls" = 0 ]; then pass N41 nntp-intake "unknown group: no file, no row, no NULL hierarchy"
else fail N41 nntp-intake "unknown group: no file, no row, no NULL hierarchy" "files=$files rows=${rows:-?} null_hierarchy=${nulls:-?}"; fi

# ---------------------------------------------------------------- N50-N59 (nntp-rfc-commands, go test, build tag e2e)
# N51 walks e2e.test.a, which N04 (POST) and N10 (TAKETHIS) filled; N56 also needs e2e.test.b.
GOLOG50="$DATA/e2e-rfc-go-test.log"
PUGLEAF_E2E_ADDR="127.0.0.1:$PORT" PUGLEAF_E2E_USER="$E2E_USER" PUGLEAF_E2E_PASS="$E2E_PASS" PUGLEAF_E2E_GROUP=e2e.test.a \
  PUGLEAF_E2E_STARTTLS="$E2E_STARTTLS" timeout 900 go test -tags e2e -count=1 -v -run '^TestE2EBinaryRFC$' ./cmd/nntp-server/ >"$GOLOG50" 2>&1
rc=$?
lines=$(grep -E '^(PASS|FAIL|INFO) N5[0-7] ' "$GOLOG50")
if [ -n "$lines" ]; then printf '%s\n' "$lines"; fi
PASSES=$((PASSES + $(printf '%s\n' "$lines" | grep -c '^PASS ')))
FAILS=$((FAILS + $(printf '%s\n' "$lines" | grep -c '^FAIL ')))
INFOS=$((INFOS + $(printf '%s\n' "$lines" | grep -c '^INFO ')))
n=$(printf '%s\n' "$lines" | grep -cE '^(PASS|FAIL) N5[0-7] ')
if [ "$n" != 8 ]; then
  fail N00 rfc "go test reported $n of 8 rfc steps" "rc=$rc; see $GOLOG50: $(tail -5 "$GOLOG50" | tr '\n' ' ')"
fi
alive || setup_fail "server died during the rfc steps"

# ---------------------------------------------------------------- N24 [nntp-session] (RFC-2): CAPABILITIES by state
pre=""; post=""; cap_detail=""
if nn_open; then
  if nn_cmd CAPABILITIES && nn_block; then pre="$NN_BLOCK"; fi
  if nn_cmd "AUTHINFO USER $E2E_USER" && nn_cmd "AUTHINFO PASS $E2E_PASS" && [ "${NN_LINE%% *}" = 281 ] \
     && nn_cmd CAPABILITIES && nn_block; then post="$NN_BLOCK"; else cap_detail="login failed: $NN_LINE"; fi
  nn_close
fi
case "$pre$post" in *"|MODE-READER|"*|*"|TAKETHIS|"*) cap_detail="$cap_detail MODE-READER/TAKETHIS listed" ;; esac
case "$pre" in *"|AUTHINFO"*) cap_detail="$cap_detail AUTHINFO on plaintext" ;; esac
case "$post" in *"|AUTHINFO"*) cap_detail="$cap_detail AUTHINFO after 281" ;; esac
if [ -n "$pre" ] && [ -n "$post" ] && [ -z "$cap_detail" ]; then pass N24 nntp-session "CAPABILITIES by state"
else fail N24 nntp-session "CAPABILITIES by state" "${cap_detail:-no reply} pre='$pre' post='$post'"; fi

# ---------------------------------------------------------------- N26 [nntp-session] (SEC-5): 481 is delayed
if nn_open && nn_cmd "AUTHINFO USER $E2E_USER"; then
  t0=$(ms_now); nn_cmd "AUTHINFO PASS wrong-password-n26" 5; el=$(( $(ms_now) - t0 )); nn_close
  if [ "${NN_LINE%% *}" = 481 ] && [ "$el" -ge 900 ]; then pass N26 nntp-session "481 delayed (${el}ms)"
  else fail N26 nntp-session "481 delayed >= 900ms" "reply='$NN_LINE' after ${el}ms"; fi
else fail N26 nntp-session "481 delayed >= 900ms" "connect failed"; fi

# ---------------------------------------------------------------- N20 [nntp-read] (SEC-2): LISTGROUP of an unknown group
before=$(find "$DATA/db" -name 'zz_no_such.db' 2>/dev/null | wc -l)
reply=""
if exec 3<>"/dev/tcp/127.0.0.1/$PORT"; then
  printf 'LISTGROUP zz.no.such\r\nQUIT\r\n' >&3
  reply=$(timeout 10 cat <&3 | tr -d '\r' | tr '\n' '|')
  exec 3>&- 3<&-
fi
after=$(find "$DATA/db" -name 'zz_no_such.db' 2>/dev/null | wc -l)
case "$reply" in *"|411 "*) r411=yes ;; *) r411=no ;; esac
if [ "$r411" = yes ] && [ "$after" = 0 ]; then pass N20 nntp-read "LISTGROUP unknown group: 411, no file"
else fail N20 nntp-read "LISTGROUP unknown group: 411, no file" "files before=$before after=$after reply='${reply:0:120}'"; fi

# ---------------------------------------------------------------- N21 [nntp-session] (SEC-3): 2 MiB command line without LF
r0=$(rss_kb); r1="$r0"; closed=no
if nn_open; then
  timeout 10 bash -c 'head -c 2097152 /dev/zero | tr "\000" A' >&3 2>/dev/null
  for _ in 1 2 3; do IFS= read -r -t 2 _l <&3; rc=$?; [ "$rc" -eq 0 ] && continue; [ "$rc" -le 128 ] && closed=yes; break; done
  r1=$(rss_kb); nn_close
fi
greets=no; if alive && nn_open; then greets=yes; nn_close; fi
grow=$(( ${r1:-0} - ${r0:-0} ))
if [ "$closed" = yes ] && [ "$greets" = yes ] && [ "$grow" -lt 16384 ]; then pass N21 nntp-session "2 MiB line: closed, RSS +${grow}kB"
else fail N21 nntp-session "2 MiB line: closed, bounded" "closed=$closed greets=$greets rss_before=${r0:-?} after=${r1:-?}"; fi

# ---------------------------------------------------------------- N23 [nntp-session] (A-nntp-sec-1): pipelined QUIT
q205=no
if nn_open; then printf 'QUIT\r\nHELP\r\n' >&3; IFS= read -r -t 5 l <&3 && case "$l" in 205*) q205=yes ;; esac; nn_close; fi
sleep 1; greets=no; if alive && nn_open; then greets=yes; nn_close; fi
if [ "$q205" = yes ] && [ "$greets" = yes ]; then pass N23 nntp-session "pipelined QUIT+HELP: 205, server survives"
else fail N23 nntp-session "pipelined QUIT+HELP" "205=$q205 alive_and_greets=$greets"; fi

# ---------------------------------------------------------------- N25 + N30 [nntp-session] (BUG-7): SIGINT with an idle client
idle=no; { exec 4<>"/dev/tcp/127.0.0.1/$PORT"; } 2>/dev/null && idle=yes
exited=no; el=0
if alive; then
  kill -INT "$PID" 2>/dev/null
  t0=$(date +%s)
  for _ in $(seq 1 30); do alive || { exited=yes; break; }; sleep 1; done
  el=$(( $(date +%s) - t0 ))
  if [ "$exited" = yes ]; then
    pass N30 nntp-session "SIGINT exits within 30s (${el}s)"
    PID=""
  else
    kill -KILL "$PID" 2>/dev/null; PID=""
    fail N30 nntp-session "SIGINT exits within 30s" "still running after 30s, killed; log tail: $(tail -5 "$LOG" | tr '\n' ' ')"
  fi
else
  fail N30 nntp-session "SIGINT exits within 30s" "server not running before the check"
  PID=""
fi
if [ "$idle" = yes ] && [ "$exited" = yes ] && [ "$el" -le 10 ]; then pass N25 nntp-session "SIGINT with an idle client exits within 10s (${el}s)"
else fail N25 nntp-session "SIGINT with an idle client exits within 10s" "idle=$idle exited=$exited el=${el}s"; fi
{ exec 4>&- 4<&-; } 2>/dev/null

# ---------------------------------------------------------------- N22 [nntp-overview] (SEC-1): XHDR huge range, fresh start
if start_server; then
  blk=no; el=0; cpu="?"; exited=no
  if nn_open; then
    nn_cmd "GROUP e2e.test.a"
    t0=$(ms_now)
    if nn_cmd "XHDR subject 1-9223372036854775807" 3 && [ "${NN_LINE%% *}" = 221 ] && nn_block 3; then blk=yes; fi
    el=$(( $(ms_now) - t0 ))
    hz=$(getconf CLK_TCK 2>/dev/null || echo 100)
    c0=$(cpu_ticks); sleep 5; c1=$(cpu_ticks)
    [ -n "$c0" ] && [ -n "$c1" ] && cpu=$(( (c1 - c0) * 100 / (hz * 5) ))
    nn_close
  fi
  kill -INT "$PID" 2>/dev/null
  for _ in $(seq 1 10); do alive || { exited=yes; break; }; sleep 1; done
  [ "$exited" = no ] && kill -KILL "$PID" 2>/dev/null; PID=""
  if [ "$blk" = yes ] && [ "$el" -le 3000 ] && [ "$cpu" != "?" ] && [ "$cpu" -lt 50 ] && [ "$exited" = yes ]; then
    pass N22 nntp-overview "XHDR huge range: block in ${el}ms, cpu ${cpu}%, SIGINT exit"
  else fail N22 nntp-overview "XHDR huge range bounded" "block=$blk ${el}ms cpu=${cpu}% exited=$exited"; fi
else fail N22 nntp-overview "fresh start for the spin probe" "server failed to start; log tail: $(tail -5 "$LOG" | tr '\n' ' ')"; fi

echo "SUMMARY pass=$PASSES fail=$FAILS info=$INFOS data=$DATA log=$LOG"
[ "$FAILS" -gt 125 ] && FAILS=125
exit "$FAILS"
