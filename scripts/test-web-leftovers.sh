#!/usr/bin/env bash
# End-to-end checks for plan web-sqlite-leftovers. Uses scratch data only.
# Usage: scripts/test-web-leftovers.sh [--build]
# Env: PORT (18990), DATA (./data-test-web-sqlite-leftovers), BIN (./build/webserver),
#      FETCHER (./build/pugleaf-fetcher), AUDIT (./build/audit-web-posts),
#      NNTPHOST (default: first FQDN that resolves here; cmd/web checks it by DNS lookup)
# Safety: the server runs with its working directory in $DATA/run (only web/ is linked there)
# and an absolute -data, so cwd-relative paths can never reach the checkout's ./data.
# Checks are tagged with the slice that makes them pass; at the plan's base commit most FAIL.
set -u
cd "$(git rev-parse --show-toplevel)" || exit 2
TOP=$(pwd -P)
PORT="${PORT:-18990}"
DATA="${DATA:-./data-test-web-sqlite-leftovers}"
BIN="${BIN:-./build/webserver}"
FETCHER="${FETCHER:-./build/pugleaf-fetcher}"
AUDIT="${AUDIT:-./build/audit-web-posts}"
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
if [ "${1:-}" = "--build" ]; then ./build_webserver.sh || exit 2; ./build_fetcher.sh || exit 2; fi
[ -x "$BIN" ] || { echo "missing $BIN (run with --build)"; exit 2; }
BIN_ABS="$(cd "$(dirname "$BIN")" && pwd -P)/$(basename "$BIN")"
FETCHER_ABS=""
[ -x "$FETCHER" ] && FETCHER_ABS="$(cd "$(dirname "$FETCHER")" && pwd -P)/$(basename "$FETCHER")"
AUDIT_ABS=""
[ -x "$AUDIT" ] && AUDIT_ABS="$(cd "$(dirname "$AUDIT")" && pwd -P)/$(basename "$AUDIT")"

rm -rf "$DATA"; mkdir -p "$DATA/run" || exit 2
DATA_ABS=$(cd "$DATA" && pwd -P)
RUN="$DATA_ABS/run"
ln -s "$TOP/web" "$RUN/web" || exit 2
LOG="$DATA/webserver.log"; DB="$DATA/cfg/pugleaf.sq3"
JAR_ADMIN="$DATA/jar_admin"; JAR_IP="$DATA/jar_ip"
PW='smoke-password-0123456789'; PASSES=0; FAILS=0; PID=""

pass() { PASSES=$((PASSES+1)); echo "PASS $1 [$2] $3"; }
fail() { FAILS=$((FAILS+1)); echo "FAIL $1 [$2] $3 :: ${4:-}"; }
info() { echo "INFO $1 [$2] $3"; }
q() { sqlite3 -cmd '.timeout 5000' "$DB" "$1"; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
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
TOK_OK=$(printf %s smoke-valid-token | sha256sum | cut -d' ' -f1)
q "INSERT OR REPLACE INTO config(key,value) VALUES('APIEnabled','true'),('AbuseMail','abuse@smoke.invalid'),('registration_enabled','true');
INSERT OR IGNORE INTO newsgroups(name,description,last_article,message_count,active,hierarchy) VALUES('smoke.test','',0,0,1,'smoke'),('smoke.inactive2','',0,0,0,'smoke');
INSERT OR IGNORE INTO ai_models(post_key,ollama_model_name,display_name,description,is_active,is_default,sort_order) VALUES('smoke','smoke','Smoke','',1,1,0);
INSERT OR IGNORE INTO api_tokens(apitoken,ownername,ownerid,expires_at,is_enabled) VALUES('$TOK_OK','smoke',0,NULL,1);
INSERT OR IGNORE INTO sections(name,display_name) VALUES('lo2sec','Leftovers');
INSERT OR IGNORE INTO section_groups(section_id,newsgroup_name) VALUES((SELECT id FROM sections WHERE name='lo2sec'),'zz.lo2.gone'),((SELECT id FROM sections WHERE name='lo2sec'),'smoke.inactive2');" \
  || { echo "seeding $DB failed"; exit 2; }
start_server || { echo "server failed to restart"; tail -60 "$LOG"; exit 2; }

# smokeadmin becomes user id 1 (admin); smokeip is an ordinary user used by L12
curl -s -o /dev/null -c "$JAR_ADMIN" -b "$JAR_ADMIN" -X POST --data-urlencode username=smokeadmin \
  --data-urlencode email=smokeadmin@smoke.invalid --data-urlencode "password1=$PW" --data-urlencode "password2=$PW" "$BASE/register"
[ "$(code -b "$JAR_ADMIN" "$BASE/profile")" = 200 ] || { echo "cannot register/log in as smokeadmin"; tail -40 "$LOG"; exit 2; }
curl -s -o /dev/null -X POST --data-urlencode username=smokeip --data-urlencode email=smokeip@smoke.invalid \
  --data-urlencode "password1=$PW" --data-urlencode "password2=$PW" "$BASE/register"
[ "$(q "SELECT count(*) FROM users WHERE username='smokeip'")" = 1 ] || { echo "cannot register smokeip"; tail -40 "$LOG"; exit 2; }

# ---------------------------------------------------------------- L01 / L02 (lo-db)
sid=$(awk '$6=="session_id"{print $7}' "$JAR_ADMIN" | tail -1)
sdb=$(q "SELECT session_id FROM users WHERE username='smokeadmin'")
p=$(code -b "$JAR_ADMIN" "$BASE/profile")
if [ -n "$sid" ] && [ "$sdb" != "$sid" ] && [ ${#sdb} = 64 ] && [ "$p" = 200 ]; then
  pass L01 lo-db "session token hashed at rest"
else
  fail L01 lo-db "session token hashed at rest" "cookie_len=${#sid} db_len=${#sdb} equal=$([ "$sdb" = "$sid" ] && echo yes || echo no) profile=$p"
fi
m=$(q "SELECT count(*) FROM schema_migrations WHERE filename LIKE '0028_main_%'")
col=$(q "SELECT count(*) FROM pragma_table_info('users') WHERE name='login_attempt_at'")
[ "$m" = 1 ] && [ "$col" = 1 ] && pass L02 lo-db "migration 0028 applied" || fail L02 lo-db "migration 0028 applied" "migration=$m column=$col"

# ---------------------------------------------------------------- L03 (lo-web-core)
u0=$(q "SELECT COALESCE(usage_count,0) FROM api_tokens WHERE apitoken='$TOK_OK'")
for _ in 1 2 3 4 5; do code -H 'X-API: smoke-valid-token' "$BASE/api/v1/groups" >/dev/null; done
stop_server
u1=$(q "SELECT COALESCE(usage_count,0) FROM api_tokens WHERE apitoken='$TOK_OK'")
lu=$(q "SELECT COALESCE(last_used_at,'') FROM api_tokens WHERE apitoken='$TOK_OK'")
if [ "$((u1-u0))" = 5 ] && [ -n "$lu" ]; then
  pass L03 lo-web-core "API token usage counted and flushed on shutdown"
else
  fail L03 lo-web-core "API token usage counted and flushed on shutdown" "before=$u0 after=$u1 last_used='$lu'"
fi
start_server || { echo "server failed to restart"; tail -60 "$LOG"; exit 2; }

# ---------------------------------------------------------------- article for L04/L05/L11
curl -s -o /dev/null -b "$JAR_ADMIN" -X POST --data-urlencode newsgroups=smoke.test \
  --data-urlencode 'subject=Leftovers smoke' --data-urlencode body=hello "$BASE/SitePostSubmit"
ART=""; GDB=""
for _ in $(seq 1 30); do
  GDB=$(find "$DATA/db" -name 'smoke_test.db' 2>/dev/null | head -1)
  if [ -n "$GDB" ]; then
    ART=$(sqlite3 -cmd '.timeout 5000' "$GDB" "SELECT article_num FROM articles WHERE subject='Leftovers smoke'" 2>/dev/null | head -1)
    [ -n "$ART" ] && break
  fi
  sleep 1
done

# ---------------------------------------------------------------- L04 / L05 (lo-web-core)
if [ -z "$ART" ]; then
  fail L04 lo-web-core "web preview route" "no article"
  fail L05 lo-web-core "API preview follows APIEnabled" "no article"
else
  b=$(curl -s -o "$DATA/preview.html" -w '%{http_code}' "$BASE/groups/smoke.test/articles/$ART/preview")
  if [ "$b" = 200 ] && grep -q 'Leftovers smoke' "$DATA/preview.html"; then
    pass L04 lo-web-core "web preview route"
  else
    fail L04 lo-web-core "web preview route" "status=$b"
  fi
  a1=$(code "$BASE/api/v1/groups/smoke.test/articles/$ART/preview")
  stop_server
  q "UPDATE config SET value='false' WHERE key='APIEnabled'"
  start_server || { echo "server failed to restart"; tail -60 "$LOG"; exit 2; }
  a2=$(code "$BASE/api/v1/groups/smoke.test/articles/$ART/preview")
  w2=$(code "$BASE/groups/smoke.test/articles/$ART/preview")
  stop_server
  q "UPDATE config SET value='true' WHERE key='APIEnabled'"
  start_server || { echo "server failed to restart"; tail -60 "$LOG"; exit 2; }
  if [ "$a1" = 200 ] && [ "$a2" = 503 ] && [ "$w2" = 200 ]; then
    pass L05 lo-web-core "API preview follows APIEnabled"
  else
    fail L05 lo-web-core "API preview follows APIEnabled" "api_on=$a1 api_off=$a2 web_off=$w2"
  fi
fi

# ---------------------------------------------------------------- L06 / L07 (lo-web-core)
js=$(curl -s "$BASE/static/js/thread-tree.js")
if printf %s "$js" | grep -q '/articles/${articleNum}/preview' && ! printf %s "$js" | grep -q '/api/v1/groups/'; then
  pass L06 lo-web-core "tree view uses the web preview route"
else
  fail L06 lo-web-core "tree view uses the web preview route" "len=${#js}"
fi
ct=$(curl -sI "$BASE/favicon.ico" | tr -d '\r' | grep -i '^content-type:' | head -1)
printf %s "$ct" | grep -qi 'image/' && pass L07 lo-web-core "favicon content type" || fail L07 lo-web-core "favicon content type" "$ct"

# ---------------------------------------------------------------- L08 / L09 (source checks)
n=$(grep -nE 'renderError\([^)]*err\.Error\(\)|gin\.H\{"Error": err\.Error\(\)\}' "$TOP"/internal/web/*.go 2>/dev/null \
  | grep -v '_test.go' | grep -v 'web_admin' | wc -l)
[ "$n" = 0 ] && pass L08 lo-web-handlers "no internal error text in visitor pages" || fail L08 lo-web-handlers "no internal error text in visitor pages" "sites=$n"
n=$(grep -c 'gin.H{"error": err.Error()}' "$TOP/internal/web/web_apiHandlers.go" 2>/dev/null || true)
[ "${n:-0}" = 0 ] && pass L09 lo-web-core "no internal error text in API JSON" || fail L09 lo-web-core "no internal error text in API JSON" "sites=$n"

# ---------------------------------------------------------------- L12 (lo2-web-server)
stop_server
q "INSERT OR REPLACE INTO config(key,value) VALUES('ReverseProxyIPHeader','X-Real-IP')"
start_server || { echo "server failed to restart"; tail -60 "$LOG"; exit 2; }
curl -s -o /dev/null -c "$JAR_IP" -b "$JAR_IP" -X POST -H 'X-Forwarded-For: 198.51.100.9' -H 'X-Real-IP: 203.0.113.7' \
  --data-urlencode username=smokeip --data-urlencode "password=$PW" "$BASE/login"
ip1=$(q "SELECT COALESCE(last_login_ip,'') FROM users WHERE username='smokeip'")
stop_server
q "DELETE FROM config WHERE key='ReverseProxyIPHeader'"
start_server || { echo "server failed to restart"; tail -60 "$LOG"; exit 2; }
rm -f "$JAR_IP"
curl -s -o /dev/null -c "$JAR_IP" -b "$JAR_IP" -X POST -H 'X-Forwarded-For: 198.51.100.9' -H 'X-Real-IP: 203.0.113.7' \
  --data-urlencode username=smokeip --data-urlencode "password=$PW" "$BASE/login"
ip2=$(q "SELECT COALESCE(last_login_ip,'') FROM users WHERE username='smokeip'")
if [ "$ip1" = 203.0.113.7 ] && [ "$ip2" = 198.51.100.9 ]; then
  pass L12 lo2-web-server "configured client IP header"
else
  fail L12 lo2-web-server "configured client IP header" "x-real-ip=$ip1 (want 203.0.113.7) default=$ip2 (want 198.51.100.9)"
fi

# ---------------------------------------------------------------- L13 (lo2-web-forms)
q "UPDATE users SET display_name='Jane <jane@smoke.invalid>' WHERE username='smokeadmin'"
curl -s -o /dev/null -b "$JAR_ADMIN" -X POST --data-urlencode email=smokeadmin2@smoke.invalid \
  --data-urlencode 'display_name=Jane <jane@smoke.invalid>' --data-urlencode "current_password=$PW" "$BASE/profile"
em=$(q "SELECT email FROM users WHERE username='smokeadmin'")
[ "$em" = smokeadmin2@smoke.invalid ] && pass L13 lo2-web-forms "legacy display name does not block a profile change" \
  || fail L13 lo2-web-forms "legacy display name does not block a profile change" "email=$em"

# ---------------------------------------------------------------- L14 / L15 (lo2-web-pages)
c1=$(code "$BASE/lo2sec/zz.lo2.gone/")
c2=$(code "$BASE/lo2sec/zz.lo2.gone/tree/1")
n=$(find "$DATA/db" -name 'zz_lo2_gone.db*' 2>/dev/null | wc -l)
c3=$(code "$BASE/lo2sec/smoke.inactive2/")
if [ "$c1" = 404 ] && [ "$c2" = 404 ] && [ "$n" = 0 ] && [ "$c3" = 404 ]; then
  pass L14 lo2-web-pages "section routes need an existing, active group"
else
  fail L14 lo2-web-pages "section routes need an existing, active group" "gone=$c1 gone_tree=$c2 files=$n inactive=$c3"
fi
c=$(code "$BASE/lo2sec/?page=100000000000000000")
[ "$c" = 200 ] && pass L15 lo2-web-pages "huge page number handled" || fail L15 lo2-web-pages "huge page number handled" "status=$c"

# ---------------------------------------------------------------- L16 (lo2-web-server)
{ printf '{"message":"'; for _ in $(seq 1 200); do printf 'a%.0s' $(seq 1 1000); done; printf '"}'; } > "$DATA/big.json"
c=$(code -b "$JAR_ADMIN" -H 'Content-Type: application/json' -X POST --data-binary @"$DATA/big.json" "$BASE/aichat/send")
[ "$c" = 413 ] && pass L16 lo2-web-server "chat request body limit" || fail L16 lo2-web-server "chat request body limit" "status=$c"

# ---------------------------------------------------------------- L10 (lo-paths), server stopped
stop_server
if [ -z "$FETCHER_ABS" ]; then
  fail L10 lo-paths "fetcher honours -data" "missing binary $FETCHER"
else
  ( cd "$RUN" && exec timeout 120 "$FETCHER_ABS" -data "$DATA_ABS" -nntphostname "$NNTPHOST" ) > "$DATA/fetcher.log" 2>&1
  rc=$?
  if [ "$rc" != 124 ] && [ -f "$DATA/progress.db/progress.db" ] && [ ! -e "$RUN/data" ]; then
    pass L10 lo-paths "fetcher honours -data"
  else
    fail L10 lo-paths "fetcher honours -data" "rc=$rc progress=$([ -f "$DATA/progress.db/progress.db" ] && echo yes || echo no) cwd_data=$([ -e "$RUN/data" ] && echo yes || echo no)"
  fi
fi

# ---------------------------------------------------------------- L11 (lo-audit-tool), server stopped
if [ -z "$AUDIT_ABS" ]; then
  fail L11 lo-audit-tool "audit tool finds injected rows and is read-only" "missing binary $AUDIT"
elif [ -z "$GDB" ]; then
  fail L11 lo-audit-tool "audit tool finds injected rows and is read-only" "no group DB"
else
  out0=$("$AUDIT_ABS" -data "$DATA" 2>&1); rc0=$?
  q "INSERT INTO post_queue(newsgroup_id, message_id, created, posted_to_remote) VALUES((SELECT id FROM newsgroups WHERE name='smoke.test'), '<inject.1@smoke.invalid>', CURRENT_TIMESTAMP, 1)"
  sqlite3 -cmd '.timeout 5000' "$GDB" "INSERT INTO articles(article_num, message_id, subject, from_header, date_sent, date_string, \"references\", bytes, lines, path, headers_json, body_text) VALUES(900001, '<inject.1@smoke.invalid>', 'x', 'Evil' || char(13,10) || 'Control: cancel <x@y>', CURRENT_TIMESTAMP, '', '', 1, 1, '.POSTED!not-for-mail', 'From: Evil' || char(13,10) || 'Control: cancel <x@y>' || char(10) || 'Subject: x', 'b')"
  h1=$(sha256sum "$DB" "$GDB")
  out1=$("$AUDIT_ABS" -data "$DATA" 2>&1); rc1=$?
  h2=$(sha256sum "$DB" "$GDB")
  if [ "$rc0" = 0 ] && printf %s "$out0" | grep -q 'flagged=0' \
     && [ "$rc1" = 1 ] && printf %s "$out1" | grep -qF '<inject.1@smoke.invalid>' && [ "$h1" = "$h2" ]; then
    pass L11 lo-audit-tool "audit tool finds injected rows and is read-only"
  else
    fail L11 lo-audit-tool "audit tool finds injected rows and is read-only" \
      "clean_rc=$rc0 dirty_rc=$rc1 hashes_equal=$([ "$h1" = "$h2" ] && echo yes || echo no)"
  fi
fi

races=$(grep -c 'WARNING: DATA RACE' "$LOG" 2>/dev/null)
info L99 any "DATA RACE reports in log: ${races:-0}"
echo "SUMMARY pass=$PASSES fail=$FAILS data=$DATA log=$LOG"
[ "$FAILS" -gt 125 ] && FAILS=125
exit "$FAILS"
