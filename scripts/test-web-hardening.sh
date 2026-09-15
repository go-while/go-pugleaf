#!/usr/bin/env bash
# End-to-end checks for plan web-sqlite-hardening. Uses scratch data only.
# Usage: scripts/test-web-hardening.sh [--build]
# Env: PORT (18980), DATA (./data-test-web-sqlite-hardening), BIN (./build/webserver),
#      NNTPHOST (default: first FQDN that resolves here; cmd/web checks it by DNS lookup)
# Safety: the server runs with its working directory in $DATA/run (only web/ is linked there)
# and an absolute -data, so cwd-relative paths can never reach the checkout's ./data.
set -u
cd "$(git rev-parse --show-toplevel)" || exit 2
TOP=$(pwd -P)
PORT="${PORT:-18980}"
DATA="${DATA:-./data-test-web-sqlite-hardening}"
BIN="${BIN:-./build/webserver}"
BASE="http://127.0.0.1:${PORT}"
case "$DATA" in *..*) echo "refusing DATA=$DATA (no .. allowed)"; exit 2 ;; esac
case "$DATA" in ./data-test-*) ;; *) echo "refusing DATA=$DATA (must start with ./data-test-)"; exit 2 ;; esac
for t in curl sqlite3 sha256sum timeout getent; do command -v "$t" >/dev/null || { echo "missing tool: $t"; exit 2; }; done
# the default config blocks User-Agents containing "curl" (BadBots): send a neutral one everywhere
curl() { command curl -A 'pugleaf-smoke/1.0' "$@"; }
# processor.SetHostname wants an FQDN (not localhost, not an IP) that resolves (net.LookupIP)
if [ -z "${NNTPHOST:-}" ]; then
  for h in "$(hostname -f 2>/dev/null)" "$(hostname 2>/dev/null).local" "$(hostname 2>/dev/null).lan"; do
    case "$h" in ""|localhost*|*[!A-Za-z0-9.-]*) continue ;; *.*) ;; *) continue ;; esac
    getent hosts "$h" >/dev/null 2>&1 && { NNTPHOST=$h; break; }
  done
fi
[ -n "${NNTPHOST:-}" ] || { echo "no resolvable FQDN for -nntphostname: set NNTPHOST=<fqdn that resolves>"; exit 2; }
# cmd/web renames ./.update and shuts down when it exists (monitorUpdateFile): never touch it
[ -e ./.update ] && { echo "refusing to run: ./.update exists in $(pwd)"; exit 2; }
if [ "${1:-}" = "--build" ]; then ./build_webserver.sh || exit 2; fi
[ -x "$BIN" ] || { echo "missing $BIN (run with --build)"; exit 2; }
BIN_ABS="$(cd "$(dirname "$BIN")" && pwd -P)/$(basename "$BIN")"

rm -rf "$DATA"; mkdir -p "$DATA/run" || exit 2
DATA_ABS=$(cd "$DATA" && pwd -P)
RUN="$DATA_ABS/run"
ln -s "$TOP/web" "$RUN/web" || exit 2
LOG="$DATA/webserver.log"; DB="$DATA/cfg/pugleaf.sq3"
JAR_REG="$DATA/jar_reg"; JAR_ADMIN="$DATA/jar_admin"; JAR_USER="$DATA/jar_user"
PW='smoke-password-0123456789'; PASSES=0; FAILS=0; PID=""

pass() { PASSES=$((PASSES+1)); echo "PASS $1 [$2] $3"; }
fail() { FAILS=$((FAILS+1)); echo "FAIL $1 [$2] $3 :: ${4:-}"; }
info() { echo "INFO $1 [$2] $3"; }
q() { sqlite3 -cmd '.timeout 5000' "$DB" "$1"; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
location() { curl -s -o /dev/null -D - "$@" | tr -d '\r' | awk 'tolower($1)=="location:"{print $2}'; }
alive() { [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; }
start_server() {
  ( cd "$RUN" && exec "$BIN_ABS" -data "$DATA_ABS" -webport "$PORT" -nntphostname "$NNTPHOST" "$@" ) >>"$LOG" 2>&1 &
  PID=$!
  for _ in $(seq 1 90); do
    curl -fs "$BASE/ping" >/dev/null 2>&1 && return 0
    alive || return 1
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
ensure_up() { alive || { PID=""; start_server || { echo "restart failed"; tail -40 "$LOG"; exit 2; }; }; }
trap stop_server EXIT

# refuse to test against anything but a free port: a running server there would get the checks
curl -fs "$BASE/ping" >/dev/null 2>&1 && { echo "refusing to run: something already answers on $BASE"; exit 2; }

# first start creates the schema; then seed while stopped (config cache is 5 min)
start_server || { echo "server failed to start"; tail -60 "$LOG"; exit 2; }
stop_server
# a binary that ignores -data would have created its DB under $RUN/data instead
[ "$(q "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='config'" 2>/dev/null)" = 1 ] \
  || { echo "server did not create the schema in $DB (does $BIN honor -data?)"; ls -la "$RUN"; exit 2; }
TOK_EXP=$(printf %s smoke-expired-token | sha256sum | cut -d' ' -f1)
TOK_OK=$(printf %s smoke-valid-token | sha256sum | cut -d' ' -f1)
q "INSERT OR REPLACE INTO config(key,value) VALUES('APIEnabled','true'),('AbuseMail','abuse@smoke.invalid'),('registration_enabled','true');
INSERT OR IGNORE INTO newsgroups(name,description,last_article,message_count,active,hierarchy) VALUES('smoke.test','',0,0,1,'smoke'),('smoke.inactive','',0,0,0,'smoke');
INSERT OR IGNORE INTO sections(name,display_name) VALUES('smokesec','Smoke');
INSERT OR IGNORE INTO ai_models(post_key,ollama_model_name,display_name,description,is_active,is_default,sort_order) VALUES('smoke','smoke','Smoke','',1,1,0);
INSERT OR IGNORE INTO api_tokens(apitoken,ownername,ownerid,expires_at,is_enabled) VALUES('$TOK_EXP','smoke',0,'2000-01-01 00:00:00',1),('$TOK_OK','smoke',0,NULL,1);" \
  || { echo "seeding $DB failed"; exit 2; }
start_server || { echo "server failed to restart"; tail -60 "$LOG"; exit 2; }

# E01 register logs in (user id 1 = smokeadmin = admin)
curl -s -o /dev/null -c "$JAR_REG" -b "$JAR_REG" -X POST --data-urlencode username=smokeadmin \
  --data-urlencode email=smokeadmin@smoke.invalid --data-urlencode "password1=$PW" --data-urlencode "password2=$PW" "$BASE/register"
p=$(code -b "$JAR_REG" "$BASE/profile")
[ "$p" = 200 ] && pass E01 w1-auth "register logs the user in" || fail E01 w1-auth "register logs the user in" "profile=$p"
curl -s -o /dev/null -X POST --data-urlencode username=smokeuser --data-urlencode email=smokeuser@smoke.invalid \
  --data-urlencode "password1=$PW" --data-urlencode "password2=$PW" "$BASE/register"
curl -s -o /dev/null -c "$JAR_ADMIN" -b "$JAR_ADMIN" -X POST --data-urlencode username=smokeadmin --data-urlencode "password=$PW" "$BASE/login"
[ "$(code -b "$JAR_ADMIN" "$BASE/profile")" = 200 ] || { echo "cannot log in as smokeadmin"; tail -40 "$LOG"; exit 2; }

# E02 login by email
loc=$(location -c "$JAR_USER" -b "$JAR_USER" -X POST --data-urlencode username=smokeuser@smoke.invalid --data-urlencode "password=$PW" "$BASE/login")
p=$(code -b "$JAR_USER" "$BASE/profile")
[ "$p" = 200 ] && pass E02 w1-auth "login by email" || fail E02 w1-auth "login by email" "location=$loc profile=$p"

# E03 same message for unknown user and wrong password
m1=$(curl -s -X POST --data-urlencode username=nosuchuser --data-urlencode password=wrong "$BASE/login" | grep -o 'Invalid username/email or password\|Login error[^<]*' | head -1)
m2=$(curl -s -X POST --data-urlencode username=smokeuser --data-urlencode password=wrong "$BASE/login" | grep -o 'Invalid username/email or password\|Login error[^<]*' | head -1)
[ -n "$m1" ] && [ "$m1" = "$m2" ] && pass E03 w1-auth "no username enumeration" || fail E03 w1-auth "no username enumeration" "unknown='$m1' wrong='$m2'"

# E04 open redirect
loc=$(location -X POST --data-urlencode username=smokeuser --data-urlencode "password=$PW" --data-urlencode redirect=https://evil.example/x "$BASE/login")
case "$loc" in //*|"") fail E04 w1-auth "login redirect stays local" "location=$loc" ;; /*) pass E04 w1-auth "login redirect stays local" ;; *) fail E04 w1-auth "login redirect stays local" "location=$loc" ;; esac

# E05 >72 byte password rejected with clear message
long=$(printf 'a%.0s' $(seq 1 80))
curl -s -X POST --data-urlencode username=smokelong --data-urlencode email=smokelong@smoke.invalid \
  --data-urlencode "password1=$long" --data-urlencode "password2=$long" "$BASE/register" | grep -q '72 bytes' \
  && pass E05 w1-auth "72-byte password limit message" || fail E05 w1-auth "72-byte password limit message"

# E06/E07 API tokens
c=$(code -H 'X-API: smoke-expired-token' "$BASE/api/v1/groups"); sleep 2
if [ "$c" = 401 ] && alive; then pass E06 w1-api "expired token -> 401, server alive"; else fail E06 w1-api "expired token -> 401, server alive" "status=$c alive=$(alive && echo yes || echo no)"; fi
ensure_up
c=$(code -H 'X-API: smoke-valid-token' "$BASE/api/v1/groups")
[ "$c" = 200 ] && pass E07 w1-api "valid token -> 200" || fail E07 w1-api "valid token -> 200" "status=$c"

# E08-E10 no group DB creation for unknown/inactive groups
c=$(code "$BASE/api/thread-tree?group=zz.smoke.fake&thread_root=1"); n=$(find "$DATA/db" -name 'zz_smoke_fake.db' 2>/dev/null | wc -l)
[ "$c" = 404 ] && [ "$n" = 0 ] && pass E08 w1-api "tree API unknown group" || fail E08 w1-api "tree API unknown group" "status=$c files=$n"
c=$(code "$BASE/api/thread-tree?group=smoke.inactive&thread_root=1")
[ "$c" = 404 ] && pass E09 w1-api "tree API inactive group (anon)" || fail E09 w1-api "tree API inactive group (anon)" "status=$c"
c=$(code "$BASE/smokesec/zz.smoke.fake2/tree/1"); n=$(find "$DATA/db" -name '*zz_smoke_fake2*' 2>/dev/null | wc -l)
[ "$c" = 404 ] && [ "$n" = 0 ] && pass E10 w1-api "section tree unknown group" || fail E10 w1-api "section tree unknown group" "status=$c files=$n"

# E11/E12 client IP
curl -s -o /dev/null -X POST -H 'X-Forwarded-For: 203.0.113.9, 198.51.100.7' --data-urlencode username=smokeuser --data-urlencode "password=$PW" "$BASE/login"
ip=$(q "SELECT last_login_ip FROM users WHERE username='smokeuser'")
[ "$ip" = 198.51.100.7 ] && pass E11 w1-server "XFF right-most untrusted" || fail E11 w1-server "XFF right-most untrusted" "ip=$ip"
curl -s -o /dev/null -c "$JAR_USER" -b "$JAR_USER" -X POST -H 'X-Real-IP: not-an-ip' --data-urlencode username=smokeuser --data-urlencode "password=$PW" "$BASE/login"
ip=$(q "SELECT last_login_ip FROM users WHERE username='smokeuser'")
[ "$ip" = 127.0.0.1 ] && pass E12 w1-server "invalid X-Real-IP ignored" || fail E12 w1-server "invalid X-Real-IP ignored" "ip=$ip"

# E13/E14 CSRF
c=$(code -b "$JAR_ADMIN" -H 'Sec-Fetch-Site: cross-site' -H 'Origin: https://evil.example' -X POST --data-urlencode name=csrfsec --data-urlencode display_name=x "$BASE/admin/sections")
n=$(q "SELECT count(*) FROM sections WHERE name='csrfsec'")
[ "$c" = 403 ] && [ "$n" = 0 ] && pass E13 w1-server "cross-site admin POST blocked" || fail E13 w1-server "cross-site admin POST blocked" "status=$c created=$n"
code -b "$JAR_ADMIN" -H 'Sec-Fetch-Site: same-origin' -X POST --data-urlencode name=smokesec2 --data-urlencode display_name=x "$BASE/admin/sections" >/dev/null
n=$(q "SELECT count(*) FROM sections WHERE name='smokesec2'")
[ "$n" = 1 ] && pass E14 w1-server "same-origin admin POST works" || fail E14 w1-server "same-origin admin POST works" "created=$n"

# E15 cross-site logout ignored
code -b "$JAR_ADMIN" -H 'Sec-Fetch-Site: cross-site' "$BASE/logout" >/dev/null
p=$(code -b "$JAR_ADMIN" "$BASE/profile")
[ "$p" = 200 ] && pass E15 w1-auth "cross-site GET /logout ignored" || fail E15 w1-auth "cross-site GET /logout ignored" "profile=$p"
if [ "$p" != 200 ]; then rm -f "$JAR_ADMIN"; curl -s -o /dev/null -c "$JAR_ADMIN" -b "$JAR_ADMIN" -X POST --data-urlencode username=smokeadmin --data-urlencode "password=$PW" "$BASE/login"; fi

# E16 no session write per request
e1=$(q "SELECT session_expires_at FROM users WHERE username='smokeadmin'")
curl -s -o /dev/null -b "$JAR_ADMIN" "$BASE/"; sleep 1; curl -s -o /dev/null -b "$JAR_ADMIN" "$BASE/"
e2=$(q "SELECT session_expires_at FROM users WHERE username='smokeadmin'")
[ -n "$e1" ] && [ "$e1" = "$e2" ] && pass E16 w1-auth "session slide throttled" || fail E16 w1-auth "session slide throttled" "before=$e1 after=$e2"

# E17 display name CR/LF rejected
curl -s -o /dev/null -b "$JAR_ADMIN" -X POST --data-urlencode email=smokeadmin@smoke.invalid \
  --data-urlencode $'display_name=Evil\r\nControl: cancel <x@y>' --data-urlencode "current_password=$PW" "$BASE/profile"
n=$(q "SELECT instr(display_name, char(10)) + instr(display_name, char(13)) FROM users WHERE username='smokeadmin'")
[ "$n" = 0 ] && pass E17 w1-auth "display name CR/LF rejected" || fail E17 w1-auth "display name CR/LF rejected" "instr=$n"

# E18 chat page must not contain the session cookie value
sid=$(awk '$6=="session_id"{print $7}' "$JAR_ADMIN" | tail -1)
body=$(curl -s -b "$JAR_ADMIN" "$BASE/aichat")
if [ -n "$sid" ] && printf %s "$body" | grep -q 'Smoke' && ! printf %s "$body" | grep -qF "$sid"; then pass E18 w1-server "session id not in chat page"; else fail E18 w1-server "session id not in chat page" "sid_len=${#sid}"; fi

# E19 sections cache race (binary built with -race)
( for _ in $(seq 1 300); do curl -s -o /dev/null "$BASE/smokesec/"; done ) & LOOP=$!
for i in $(seq 1 20); do code -b "$JAR_ADMIN" -H 'Sec-Fetch-Site: same-origin' -X POST --data-urlencode "name=race$i" --data-urlencode display_name=race "$BASE/admin/sections" >/dev/null; done
wait "$LOOP"
if alive && ! grep -A40 'DATA RACE\|concurrent map' "$LOG" | grep -q 'loadSectionsCache\|isValidSection\|SectionsCache\|sectionsCache'; then pass E19 w1-server "sections cache race-free"; else fail E19 w1-server "sections cache race-free" "alive=$(alive && echo yes || echo no)"; fi
ensure_up
info E22 any "DATA RACE reports in log so far: $(grep -c 'WARNING: DATA RACE' "$LOG")"

# E20 slowloris dropped
start=$SECONDS
timeout 30 bash -c "exec 3<>/dev/tcp/127.0.0.1/$PORT; printf 'GET / HTTP/1.1\r\nHost: smoke\r\n' >&3; cat <&3 >/dev/null"; rc=$?
el=$((SECONDS-start))
[ "$rc" -ne 124 ] && [ "$el" -le 20 ] && pass E20 w1-server "partial headers dropped (${el}s)" || fail E20 w1-server "partial headers dropped" "rc=$rc elapsed=${el}s"

# E21 LIKE wildcards escaped
curl -s "$BASE/search?q=%25&searchType=groups" | grep -q 'smoke.test' && fail E21 w2-dbperf "search % is literal" || pass E21 w2-dbperf "search % is literal"

# E24 throughput (informational)
if command -v ab >/dev/null; then info E24 w2-templates "$(ab -q -n 300 -c 10 "$BASE/groups" 2>/dev/null | grep 'Requests per second')"; fi

# E25/E26 posting header validation
b=$(curl -s -b "$JAR_ADMIN" -X POST --data-urlencode newsgroups=smoke.test --data-urlencode subject='Re: x' --data-urlencode body=hello \
  --data-urlencode reply_to=1 --data-urlencode $'message_id=<a@b>\r\nControl: cancel <x@y>' "$BASE/SitePostSubmit")
printf %s "$b" | grep -qi 'invalid reply message-id' && pass E25 w1-api "reply message-id CR/LF rejected" || fail E25 w1-api "reply message-id CR/LF rejected"
b=$(curl -s -b "$JAR_ADMIN" -X POST --data-urlencode newsgroups=smoke.test --data-urlencode $'subject=Hi\r\nApproved: yes' --data-urlencode body=hello "$BASE/SitePostSubmit")
printf %s "$b" | grep -qi 'subject contains invalid characters' && pass E26 w1-api "subject CR/LF rejected" || fail E26 w1-api "subject CR/LF rejected"

# E27 new group DB uses WAL
code -H 'X-API: smoke-valid-token' "$BASE/api/v1/groups/smoke.test/overview" >/dev/null
f=$(find "$DATA/db" -name 'smoke_test.db' | head -1)
jm=$( [ -n "$f" ] && sqlite3 "$f" 'PRAGMA journal_mode' )
[ "$jm" = wal ] && pass E27 w1-sqlite "new group DB journal_mode=wal" || fail E27 w1-sqlite "new group DB journal_mode=wal" "file=$f mode=$jm"

# E23 -no-cronjobs starts
stop_server
if start_server -no-cronjobs; then sleep 5; alive && curl -fs "$BASE/ping" >/dev/null && pass E23 w1-server "-no-cronjobs starts" || fail E23 w1-server "-no-cronjobs starts" "died after start"; else fail E23 w1-server "-no-cronjobs starts" "did not start"; fi
stop_server

echo "SUMMARY pass=$PASSES fail=$FAILS data=$DATA log=$LOG"
[ "$FAILS" -gt 125 ] && FAILS=125
exit "$FAILS"
