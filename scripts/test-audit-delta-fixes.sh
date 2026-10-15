#!/usr/bin/env bash
# End-to-end checks for plan audit-delta-fixes. Uses scratch data only.
# Usage: scripts/test-audit-delta-fixes.sh
# Env: PORT (18998), DATA (./data-test-audit-delta-fixes), BIN (./build/webserver),
#      RDB (./build/recover-db), FET (./build/pugleaf-fetcher),
#      NNTPHOST (default: first FQDN that resolves here; cmd/web checks it by DNS lookup)
# Safety: the web server runs with its working directory in $DATA/run (only web/ is linked there)
# and an absolute -data; the other binaries run from $DATA/run with an absolute -data too,
# so a regression that ignores -data can only write inside $DATA.
# Checks are tagged with the slice that makes them pass; at the plan's base commit most FAIL.
set -u
cd "$(git rev-parse --show-toplevel)" || exit 2
TOP=$(pwd -P)
PORT="${PORT:-18998}"
DATA="${DATA:-./data-test-audit-delta-fixes}"
BIN="${BIN:-./build/webserver}"
RDB="${RDB:-./build/recover-db}"
FET="${FET:-./build/pugleaf-fetcher}"
BASE="http://127.0.0.1:${PORT}"
case "$DATA" in *..*) echo "refusing DATA=$DATA (no .. allowed)"; exit 2 ;; esac
case "$DATA" in ./data-test-*) ;; *) echo "refusing DATA=$DATA (must start with ./data-test-)"; exit 2 ;; esac
for t in curl sqlite3 md5sum timeout getent; do command -v "$t" >/dev/null || { echo "missing tool: $t"; exit 2; }; done
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
[ -x "$BIN" ] || { echo "missing $BIN (./build_webserver.sh)"; exit 2; }
BIN_ABS="$(cd "$(dirname "$BIN")" && pwd -P)/$(basename "$BIN")"
RDB_ABS=""; [ -x "$RDB" ] && RDB_ABS="$(cd "$(dirname "$RDB")" && pwd -P)/$(basename "$RDB")"
FET_ABS=""; [ -x "$FET" ] && FET_ABS="$(cd "$(dirname "$FET")" && pwd -P)/$(basename "$FET")"

# refuse to test against anything but a free port: a running server there would get the checks
curl -fs "$BASE/ping" >/dev/null 2>&1 && { echo "refusing to run: something already answers on $BASE"; exit 2; }

rm -rf "$DATA" "$DATA-other"; mkdir -p "$DATA/run" || exit 2
DATA_ABS=$(cd "$DATA" && pwd -P)
RUN="$DATA_ABS/run"
ln -s "$TOP/web" "$RUN/web" || exit 2
LOG="$DATA/webserver.log"; DB="$DATA/cfg/pugleaf.sq3"
JAR_ADMIN="$DATA/jar_admin"; JAR_USER="$DATA/jar_user"
PW='smoke-password-0123456789'; PASSES=0; FAILS=0; INFOS=0; PID=""

pass() { PASSES=$((PASSES+1)); echo "PASS $1 [$2] $3"; }
fail() { FAILS=$((FAILS+1)); echo "FAIL $1 [$2] $3 :: ${4:-}"; }
info() { INFOS=$((INFOS+1)); echo "INFO $1 [$2] $3"; }
q() { sqlite3 -cmd '.timeout 5000' "$DB" "$1"; }
gq() { sqlite3 -cmd '.timeout 5000' "$1" "$2"; }
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
restart_or_die() { start_server || { echo "server failed to restart"; tail -60 "$LOG"; exit 2; }; }
trap stop_server EXIT
group_db() { find "$DATA/db" -name "$1" 2>/dev/null | head -1; }

# first start creates the schema; then seed while stopped (config cache is 5 min)
start_server || { echo "server failed to start"; tail -60 "$LOG"; exit 2; }
stop_server
# a binary that ignores -data would have created its DB under $RUN/data instead
[ "$(q "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='config'" 2>/dev/null)" = 1 ] \
  || { echo "server did not create the schema in $DB (does $BIN honor -data?)"; ls -la "$RUN"; exit 2; }
q "INSERT OR REPLACE INTO config(key,value) VALUES('APIEnabled','false'),('AbuseMail','abuse@smoke.invalid'),('registration_enabled','true');
INSERT OR IGNORE INTO newsgroups(name,description,last_article,message_count,active,hierarchy) VALUES('adf.e2e.a','',0,0,1,'adf'),('adf.e2e.b','',0,0,1,'adf'),('adf.e2e.c','',0,0,1,'adf');
INSERT INTO cron_jobs(name,command,interval_minutes,start_hour_minute,enabled) VALUES('adf-e2e-disabled','true',1440,'03:00',0),('adf-e2e-enabled','true',1440,'03:00',1);" \
  || { echo "seeding $DB failed"; exit 2; }
restart_or_die   # no -edit-cronjobs

# adfadmin becomes user id 1 (admin); adfuser is an ordinary user (E08 posts as it)
curl -s -o /dev/null -c "$JAR_ADMIN" -b "$JAR_ADMIN" -X POST --data-urlencode username=adfadmin \
  --data-urlencode email=adfadmin@smoke.invalid --data-urlencode "password1=$PW" --data-urlencode "password2=$PW" "$BASE/register"
[ "$(code -b "$JAR_ADMIN" "$BASE/profile")" = 200 ] || { echo "cannot register/log in as adfadmin"; tail -40 "$LOG"; exit 2; }
curl -s -o /dev/null -c "$JAR_USER" -b "$JAR_USER" -X POST --data-urlencode username=adfuser \
  --data-urlencode email=adfuser@smoke.invalid --data-urlencode "password1=$PW" --data-urlencode "password2=$PW" "$BASE/register"
[ "$(code -b "$JAR_USER" "$BASE/profile")" = 200 ] || { echo "cannot register/log in as adfuser"; tail -40 "$LOG"; exit 2; }

# ---------------------------------------------------------------- E02 / E03 (adf-web-cron)
c=$(code -b "$JAR_ADMIN" -X POST --data-urlencode name=adf-e2e-new --data-urlencode command=true \
  --data-urlencode interval_minutes=1440 --data-urlencode start_hour_minute=03:00 --data-urlencode enabled=on "$BASE/admin/crons/create")
n=$(q "SELECT count(*) FROM cron_jobs WHERE name='adf-e2e-new'")
[ "$n" = 0 ] && pass E02 adf-web-cron "cron create refused without -edit-cronjobs" \
  || fail E02 adf-web-cron "cron create refused without -edit-cronjobs" "status=$c rows=$n"
id_dis=$(q "SELECT id FROM cron_jobs WHERE name='adf-e2e-disabled'")
id_en=$(q "SELECT id FROM cron_jobs WHERE name='adf-e2e-enabled'")
c1=$(code -b "$JAR_ADMIN" -X POST --data-urlencode "cron_id=$id_dis" "$BASE/admin/crons/toggle")
c2=$(code -b "$JAR_ADMIN" -X POST --data-urlencode "cron_id=$id_en" "$BASE/admin/crons/toggle")
e1=$(q "SELECT enabled FROM cron_jobs WHERE id=$id_dis")
e2=$(q "SELECT enabled FROM cron_jobs WHERE id=$id_en")
# the second toggle (disabling) is the control that the admin POST path works at all
if [ "$e1" = 0 ] && [ "$e2" = 0 ]; then
  pass E03 adf-web-cron "cron enable refused, disable allowed, without -edit-cronjobs"
else
  fail E03 adf-web-cron "cron enable refused, disable allowed, without -edit-cronjobs" "disabled_job=$e1 (want 0) enabled_job=$e2 (want 0) status=$c1/$c2"
fi

# ---------------------------------------------------------------- E04 / E05 (adf-web-xss)
csp=$(curl -sD- -o /dev/null "$BASE/" | tr -d '\r' | grep -i '^content-security-policy:' | head -1)
if printf %s "$csp" | grep -q "default-src 'self'" && printf %s "$csp" | grep -q "connect-src 'self'"; then
  pass E04 adf-web-xss "Content-Security-Policy header"
else
  fail E04 adf-web-xss "Content-Security-Policy header" "header='$csp'"
fi
js=$(curl -s "$BASE/static/js/thread-tree.js")
if printf %s "$js" | grep -q 'textContent' && ! printf %s "$js" | grep -qF '${cleanPreview}'; then
  pass E05 adf-web-xss "thread-tree.js renders the preview as text"
else
  fail E05 adf-web-xss "thread-tree.js renders the preview as text" "len=${#js} textContent=$(printf %s "$js" | grep -c textContent) cleanPreview=$(printf %s "$js" | grep -cF '${cleanPreview}')"
fi

# ---------------------------------------------------------------- E08 (adf-web-post): adfuser posts to adf.e2e.b
curl -s -o /dev/null -b "$JAR_USER" -X POST --data-urlencode newsgroups=adf.e2e.b \
  --data-urlencode 'subject=ADF web post' --data-urlencode body=hello "$BASE/SitePostSubmit"
GDB_B=""; HJ=""
for _ in $(seq 1 20); do
  GDB_B=$(group_db adf_e2e_b.db)
  if [ -n "$GDB_B" ]; then
    HJ=$(gq "$GDB_B" "SELECT headers_json FROM articles WHERE subject='ADF web post'" 2>/dev/null)
    [ -n "$HJ" ] && break
  fi
  sleep 1
done
if [ -z "$HJ" ]; then
  fail E08 adf-web-post "web post stores the full header set" "no article row within 20 s (db=$GDB_B)"
else
  missing=""
  for h in 'Message-ID:' 'Date:' 'Subject:' 'Path:' 'Injection-Info:'; do printf %s "$HJ" | grep -q "^$h" || missing="$missing $h"; done
  bad=""
  printf %s "$HJ" | grep -q '^X-pugleaf-Trace' && bad="$bad X-pugleaf-Trace"
  printf %s "$HJ" | grep -q '^Bytes:' && bad="$bad Bytes:"
  if [ -z "$missing" ] && [ -z "$bad" ]; then
    pass E08 adf-web-post "web post stores the full header set"
  else
    fail E08 adf-web-post "web post stores the full header set" "missing=[$missing] present=[$bad]"
  fi
fi

# ---------------------------------------------------------------- E06 / E07 (adf-web-hide): hidden article in adf.e2e.a
code "$BASE/groups/adf.e2e.a/" >/dev/null    # makes the server create the group DB
stop_server
GDB_A=$(group_db adf_e2e_a.db)
MID='<adf-e2e-hidden-1@test.invalid>'
if [ -z "$GDB_A" ]; then
  fail E06 adf-web-hide "hidden article is 404 for visitors" "no group DB for adf.e2e.a"
  fail E07 adf-web-hide "/api/thread-tree follows APIEnabled" "no group DB for adf.e2e.a"
  restart_or_die
else
  gq "$GDB_A" "INSERT INTO articles(article_num, message_id, subject, from_header, date_sent, date_string, \"references\", bytes, lines, path, headers_json, body_text, downloaded, spam, hide) VALUES(1, '$MID', 'ADF hidden subject', 'adf <adf@test.invalid>', '2024-01-01 10:00:00', 'Mon, 01 Jan 2024 10:00:00 +0000', '', 120, 1, '.POSTED!not-for-mail', 'Subject: ADF hidden subject' || char(10) || 'Message-ID: $MID', 'hidden body', 1, 0, 1);
INSERT INTO threads(root_article, parent_article, child_article, depth, thread_order) VALUES(1, NULL, 1, 0, 0);" || echo "seeding $GDB_A failed"
  q "UPDATE newsgroups SET last_article=1, message_count=1 WHERE name='adf.e2e.a'"
  restart_or_die
  MIDU='%3Cadf-e2e-hidden-1%40test.invalid%3E'
  a1=$(code "$BASE/groups/adf.e2e.a/articles/1")
  a2=$(code "$BASE/groups/adf.e2e.a/message/$MIDU")
  a3=$(code "$BASE/groups/adf.e2e.a/articles/1/preview")
  a4=$(code -b "$JAR_ADMIN" "$BASE/groups/adf.e2e.a/articles/1")
  if [ "$a1" = 404 ] && [ "$a2" = 404 ] && [ "$a3" = 404 ] && [ "$a4" = 200 ]; then
    pass E06 adf-web-hide "hidden article is 404 for visitors, 200 for the admin"
  else
    fail E06 adf-web-hide "hidden article is 404 for visitors, 200 for the admin" "number=$a1 message=$a2 preview=$a3 admin=$a4"
  fi
  t=$(code "$BASE/api/thread-tree?group=adf.e2e.a&thread_root=1")
  [ "$t" != 200 ] && pass E07 adf-web-hide "/api/thread-tree follows APIEnabled (disabled)" \
    || fail E07 adf-web-hide "/api/thread-tree follows APIEnabled (disabled)" "status=$t"
fi

# ---------------------------------------------------------------- E18 INFO (adf-db-batch)
if [ -n "$GDB_B" ]; then
  n=$(gq "$GDB_B" "SELECT count(*) FROM sqlite_master WHERE name='article_num_seq'")
  info E18 adf-db-batch "article_num_seq in adf_e2e_b.db: $n (want 1 after wave 2)"
else
  info E18 adf-db-batch "article_num_seq: no adf_e2e_b.db"
fi
info E17 adf-db-batch "article-number reuse is covered by TestAdfBatchNumbersNotReused"

# ---------------------------------------------------------------- E09 (adf-db-readers): NULL-hierarchy row
stop_server
# the repair migration ran on the empty table at the first start: drop its row so the restart runs it again
q "INSERT INTO newsgroups(name,description,last_article,message_count,active,hierarchy,status) VALUES('adf.e2e.nullhier','',NULL,NULL,1,NULL,NULL);
DELETE FROM schema_migrations WHERE filename LIKE '0029_main_%';" || echo "seeding nullhier failed"
if start_server; then
  h=$(q "SELECT COALESCE(hierarchy,'NULL') FROM newsgroups WHERE name='adf.e2e.nullhier'")
  n=$(q "SELECT count(*) FROM newsgroups WHERE hierarchy IS NULL")
  l=$(code "$BASE/groups/")
  if [ "$h" = adf ] && [ "$n" = 0 ] && [ "$l" = 200 ]; then
    pass E09 adf-db-readers "NULL-hierarchy row repaired and readable"
  else
    fail E09 adf-db-readers "NULL-hierarchy row repaired and readable" "hierarchy=$h null_rows=$n groups_page=$l"
  fi
  q "DELETE FROM newsgroups WHERE name='adf.e2e.nullhier'"   # E11 needs a table every binary can read
else
  fail E09 adf-db-readers "NULL-hierarchy row repaired and readable" "server did not restart with the NULL row"
  stop_server
  q "DELETE FROM newsgroups WHERE name='adf.e2e.nullhier'"
  restart_or_die
fi

# ---------------------------------------------------------------- E01 (adf-web-cmd): SIGTERM = graceful shutdown
before=$(grep -c 'Graceful shutdown completed' "$LOG" 2>/dev/null)
kill -TERM "$PID" 2>/dev/null
gone=no
for _ in $(seq 1 30); do alive || { gone=yes; break; }; sleep 1; done
after=$(grep -c 'Graceful shutdown completed' "$LOG" 2>/dev/null)
if [ "$gone" = yes ] && [ "${after:-0}" -gt "${before:-0}" ]; then
  pass E01 adf-web-cmd "SIGTERM runs the graceful shutdown"
else
  fail E01 adf-web-cmd "SIGTERM runs the graceful shutdown" "gone=$gone graceful_lines=${before:-0}->${after:-0}"
fi
alive && stop_server; PID=""

# ---------------------------------------------------------------- E10 (adf-web-cmd): NNTP flags are ignored
LOG10="$DATA/webserver-nntpflags.log"
( cd "$RUN" && exec "$BIN_ABS" -data "$DATA_ABS" -webport "$((PORT+1))" -nntphostname "$NNTPHOST" -nntptcpport 21998 ) >"$LOG10" 2>&1 &
P10=$!
for _ in $(seq 1 60); do curl -fs "http://127.0.0.1:$((PORT+1))/ping" >/dev/null 2>&1 && break; kill -0 "$P10" 2>/dev/null || break; sleep 1; done
nntp=$(code "http://127.0.0.1:21998/" 2>/dev/null); nntp_listen=no
( exec 3<>/dev/tcp/127.0.0.1/21998 ) 2>/dev/null && nntp_listen=yes
kill -INT "$P10" 2>/dev/null; for _ in $(seq 1 60); do kill -0 "$P10" 2>/dev/null || break; sleep 1; done; kill -KILL "$P10" 2>/dev/null
if grep -q 'are ignored' "$LOG10" && [ "$nntp_listen" = no ]; then
  pass E10 adf-web-cmd "-nntptcpport is logged as ignored and nothing listens"
else
  fail E10 adf-web-cmd "-nntptcpport is logged as ignored and nothing listens" "ignored_line=$(grep -c 'are ignored' "$LOG10") listening=$nntp_listen"
fi

# ---------------------------------------------------------------- E11 (adf-web-cmd): -disable-active refuses an empty keep set, honours dry-run, accepts 1-field lines
active0=$(q "SELECT count(*) FROM newsgroups WHERE active=1")
printf '# newsgroups.descriptions header only\n\n' > "$DATA/active-empty.txt"
printf 'adf.e2e.a\n' > "$DATA/active-one.txt"
( cd "$RUN" && exec timeout 120 "$BIN_ABS" -data "$DATA_ABS" -webport "$((PORT+2))" -nntphostname "$NNTPHOST" -disable-active "$DATA_ABS/active-empty.txt" ) >"$DATA/disable-active-1.log" 2>&1
rc1=$?
active1=$(q "SELECT count(*) FROM newsgroups WHERE active=1")
q "UPDATE newsgroups SET active=1 WHERE name LIKE 'adf.e2e.%'"   # undo a baseline regression before the next run
( cd "$RUN" && exec timeout 120 "$BIN_ABS" -data "$DATA_ABS" -webport "$((PORT+2))" -nntphostname "$NNTPHOST" -disable-active "$DATA_ABS/active-one.txt" -disable-active-dry-run ) >"$DATA/disable-active-2.log" 2>&1
rc2=$?
active2=$(q "SELECT count(*) FROM newsgroups WHERE active=1")
q "UPDATE newsgroups SET active=1 WHERE name LIKE 'adf.e2e.%'"
( cd "$RUN" && exec timeout 120 "$BIN_ABS" -data "$DATA_ABS" -webport "$((PORT+2))" -nntphostname "$NNTPHOST" -disable-active "$DATA_ABS/active-one.txt" ) >"$DATA/disable-active-3.log" 2>&1
rc3=$?
kept=$(q "SELECT group_concat(name) FROM newsgroups WHERE active=1 ORDER BY name")
q "UPDATE newsgroups SET active=1 WHERE name LIKE 'adf.e2e.%'"
if [ "$rc1" != 0 ] && [ "$active1" = "$active0" ] && [ "$rc2" = 0 ] && [ "$active2" = "$active0" ] && [ "$rc3" = 0 ] && [ "$kept" = adf.e2e.a ]; then
  pass E11 adf-web-cmd "-disable-active: empty keep set refused, dry-run changes nothing, 1-field list applied"
else
  fail E11 adf-web-cmd "-disable-active: empty keep set refused, dry-run changes nothing, 1-field list applied" "empty: rc=$rc1 active=$active0->$active1; dry-run: rc=$rc2 active=$active0->$active2; applied: rc=$rc3 kept='$kept' (want adf.e2e.a)"
fi

# ---------------------------------------------------------------- E12 (adf-web-cmd): -useshorthashlen out of range
mkdir -p "$DATA/fresh"
LOG12="$DATA/webserver-fresh.log"
( cd "$RUN" && exec "$BIN_ABS" -data "$DATA_ABS/fresh" -webport "$((PORT+3))" -nntphostname "$NNTPHOST" -useshorthashlen 9 ) >"$LOG12" 2>&1 &
P12=$!; up=no
for _ in $(seq 1 60); do curl -fs "http://127.0.0.1:$((PORT+3))/ping" >/dev/null 2>&1 && { up=yes; break; }; kill -0 "$P12" 2>/dev/null || break; sleep 1; done
kill -INT "$P12" 2>/dev/null; for _ in $(seq 1 60); do kill -0 "$P12" 2>/dev/null || break; sleep 1; done; kill -KILL "$P12" 2>/dev/null
[ "$up" = yes ] && pass E12 adf-web-cmd "-useshorthashlen 9 does not abort the first start" \
  || fail E12 adf-web-cmd "-useshorthashlen 9 does not abort the first start" "$(grep -i 'useshorthashlen\|FATAL' "$LOG12" | tail -2 | tr '\n' ' ')"

# ---------------------------------------------------------------- E13 (adf-tools-fetcher): list update honours -data (server stopped)
if [ -z "$FET_ABS" ]; then
  fail E13 adf-tools-fetcher "-fetch-newsgroups-from-remote honours -data" "missing binary $FET"
elif [ "$(q "SELECT count(*) FROM providers WHERE enabled=1")" != 0 ]; then
  fail E13 adf-tools-fetcher "-fetch-newsgroups-from-remote honours -data" "enabled providers exist in the scratch DB: not running the fetcher"
else
  ( cd "$RUN" && exec timeout 60 "$FET_ABS" -data "$DATA_ABS" -nntphostname "$NNTPHOST" -fetch-newsgroups-from-remote '$all' ) >"$DATA/fetcher.log" 2>&1
  rc=$?
  if [ "$rc" != 0 ] && grep -qi 'no enabled providers' "$DATA/fetcher.log" && [ ! -e "$RUN/data" ] && [ -f "$DB" ]; then
    pass E13 adf-tools-fetcher "-fetch-newsgroups-from-remote honours -data"
  else
    fail E13 adf-tools-fetcher "-fetch-newsgroups-from-remote honours -data" "rc=$rc msg=$(grep -ci 'no enabled providers' "$DATA/fetcher.log") cwd_data=$([ -e "$RUN/data" ] && echo yes || echo no)"
  fi
  rm -rf "$RUN/data"
fi

# ---------------------------------------------------------------- E14 / E15 (adf-ops-recover), server stopped
if [ -z "$RDB_ABS" ]; then
  fail E14 adf-ops-recover "recover-db directory flags" "missing binary $RDB"
  fail E15 adf-ops-recover "-rewritedates keeps flags and skips unparseable dates" "missing binary $RDB"
else
  ( cd "$RUN" && exec timeout 120 "$RDB_ABS" -db "$DATA_ABS" -data "$DATA_ABS-other" -group adf.e2e.a ) >"$DATA/recover-1.log" 2>&1
  rc1=$?
  other=no; [ -e "$DATA_ABS-other" ] && other=yes
  rm -rf "$DATA_ABS-other"
  GDB_A=$(group_db adf_e2e_a.db)
  h1=$(md5sum "$GDB_A" 2>/dev/null | cut -d' ' -f1)
  ( cd "$RUN" && exec timeout 120 "$RDB_ABS" -data "$DATA_ABS" -group adf.e2e.a -repair ) >"$DATA/recover-2.log" 2>&1
  rc2=$?
  h2=$(md5sum "$GDB_A" 2>/dev/null | cut -d' ' -f1)
  if [ "$rc1" = 2 ] && [ "$other" = no ] && [ "$rc2" = 2 ] && [ "$h1" = "$h2" ]; then
    pass E14 adf-ops-recover "recover-db: -db/-data disagreement exits 2, -repair needs -force"
  else
    fail E14 adf-ops-recover "recover-db: -db/-data disagreement exits 2, -repair needs -force" "disagree_rc=$rc1 other_dir=$other repair_rc=$rc2 db_changed=$([ "$h1" = "$h2" ] && echo no || echo yes)"
  fi
  if [ -z "$GDB_B" ]; then
    fail E15 adf-ops-recover "-rewritedates keeps flags and skips unparseable dates" "no adf_e2e_b.db"
  else
    gq "$GDB_B" "INSERT INTO articles(article_num, message_id, subject, from_header, date_sent, date_string, \"references\", bytes, lines, path, headers_json, body_text, downloaded, spam, hide) VALUES
      (900001, '<adf-e2e-date-ok@test.invalid>', 'date ok', 'adf <adf@test.invalid>', '2020-01-01 00:00:00', 'Wed, 01 Jan 2020 10:00:00 +0000', '', 10, 1, '.POSTED!not-for-mail', 'Subject: date ok', 'b', 1, 1, 1),
      (900002, '<adf-e2e-date-bad@test.invalid>', 'date bad', 'adf <adf@test.invalid>', '2021-05-05 05:05:05', 'not a date at all', '', 10, 1, '.POSTED!not-for-mail', 'Subject: date bad', 'b', 1, 1, 1);" || echo "seeding dates failed"
    ( cd "$RUN" && exec timeout 120 "$RDB_ABS" -data "$DATA_ABS" -group adf.e2e.b -parsedates -rewritedates -force ) >"$DATA/recover-3.log" 2>&1
    rc3=$?
    r1=$(gq "$GDB_B" "SELECT date_sent || ' ' || spam || ' ' || hide FROM articles WHERE article_num=900001")
    r2=$(gq "$GDB_B" "SELECT date_sent || ' ' || spam || ' ' || hide FROM articles WHERE article_num=900002")
    if [ "$r1" = '2020-01-01 10:00:00 1 1' ] && [ "$r2" = '2021-05-05 05:05:05 1 1' ]; then
      pass E15 adf-ops-recover "-rewritedates keeps flags and skips unparseable dates"
    else
      fail E15 adf-ops-recover "-rewritedates keeps flags and skips unparseable dates" "rc=$rc3 ok_row='$r1' (want '2020-01-01 10:00:00 1 1') bad_row='$r2' (want '2021-05-05 05:05:05 1 1')"
    fi
  fi
fi

# ---------------------------------------------------------------- E16 (adf-ops-scripts): scripts
race=$(grep -nE 'go build[^$]*-race' build_*.sh | wc -l)
syntax=ok; bash -n run_web.sh run_web_ssl.sh go_run_web.sh 2>/dev/null || syntax=bad
flags=$(sed -n 3p run_web_ssl.sh | sed 's/^[^ ]* //')
# shellcheck disable=SC2086
( cd "$RUN" && exec "$BIN_ABS" $flags -help ) >/dev/null 2>&1; hrc=$?
if [ "$race" = 0 ] && [ "$syntax" = ok ] && [ "$hrc" = 0 ]; then
  pass E16 adf-ops-scripts "build scripts without -race, run scripts parse and use real flags"
else
  fail E16 adf-ops-scripts "build scripts without -race, run scripts parse and use real flags" "race_lines=$race syntax=$syntax run_web_ssl_flags_help_rc=$hrc"
fi

# ---------------------------------------------------------------- E19 (all)
races=$(grep -A3 'WARNING: DATA RACE' "$LOG" 2>/dev/null | grep -cE 'internal/(web|database|cache|models)')
[ "${races:-0}" = 0 ] && pass E19 all "no DATA RACE in web/database/cache/models" || fail E19 all "no DATA RACE in web/database/cache/models" "frames=$races"

echo "SUMMARY pass=$PASSES fail=$FAILS info=$INFOS data=$DATA log=$LOG"
[ "$FAILS" -gt 125 ] && FAILS=125
exit "$FAILS"
