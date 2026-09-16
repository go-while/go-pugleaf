// Command audit-web-posts is a read-only audit of web posts that can carry
// injected header lines (CR/LF smuggled into the display name, the subject or a
// reply message-id). Web posts accepted before the header validation landed may
// still hold such values; this tool finds them so the user can decide what to do.
//
// The tool never writes: it opens the main database and every group database
// with the plain sqlite3 driver and the DSN "file:<path>?mode=ro", it never
// calls database.OpenDatabase (which migrates and writes) and it never issues
// CREATE, INSERT, UPDATE or DELETE. A group database that does not exist is
// counted as missing and skipped, never created.
//
// Output is one TSV line per finding
// (group, article_num, message_id, posted_to_remote, created, field, reason)
// followed by "checked=<n> flagged=<n> missing=<n>".
// Exit code 0: nothing flagged. 1: something flagged. 2: error.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-while/go-pugleaf/internal/database"
	_ "github.com/mattn/go-sqlite3" // SQLite3 driver
)

// appVersion is set by build_audit-web-posts.sh via -ldflags.
var appVersion string

// auditMessageIDRe is the message-id form accepted for web posts
// (same expression as internal/web/web_sitePostPage.go postMessageIDRe).
var auditMessageIDRe = regexp.MustCompile(`^<[^<>\s@]+@[^<>\s@]+>$`)

// allowedHeaderNames are the header names that sitePostSubmit writes into
// Article.HeadersJSON (internal/web/web_sitePostPage.go:356-369 at the base
// commit of this tool): "MIME-Version", "Content-Type",
// "Content-Transfer-Encoding", "X-pugleaf-Trace" (followed by two continuation
// lines that start with a space), "From", "Newsgroups", "Lines" and "Bytes".
// Anything else in a web post's headers_json was injected.
var allowedHeaderNames = map[string]bool{
	"mime-version":              true,
	"content-type":              true,
	"content-transfer-encoding": true,
	"x-pugleaf-trace":           true,
	"from":                      true,
	"newsgroups":                true,
	"lines":                     true,
	"bytes":                     true,
}

// singleHeaderNames must appear at most once in a post's header lines.
var singleHeaderNames = map[string]bool{
	"from":       true,
	"subject":    true,
	"newsgroups": true,
	"message-id": true,
}

// queueRow is one post_queue entry joined to its newsgroup name.
type queueRow struct {
	group          string
	messageID      string
	created        string
	postedToRemote string
}

// articleRow holds the article fields the audit inspects.
type articleRow struct {
	articleNum  int64
	messageID   string
	subject     string
	fromHeader  string
	references  string
	headersJSON string
}

// finding is one flagged field of one article.
type finding struct {
	field  string
	reason string
}

const selectArticleFields = `SELECT article_num, message_id, subject, from_header, "references", headers_json FROM articles`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the tool body, split out so tests can drive it.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("audit-web-posts", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data", "./data", "Data directory (read-only)")
	all := fs.Bool("all", false, "Scan every article of the groups that have post_queue rows, not only the queued message-ids")
	verbose := fs.Bool("v", false, "Verbose progress on stderr")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *verbose && appVersion != "" {
		fmt.Fprintf(stderr, "[AUDIT] audit-web-posts %s\n", appVersion)
	}

	mainPath := filepath.Join(*dataDir, "cfg", "pugleaf.sq3")
	mainDB, err := openReadOnly(mainPath)
	if err != nil {
		fmt.Fprintf(stderr, "[AUDIT] failed to open main database %s: %v\n", mainPath, err)
		return 2
	}
	defer func() {
		if cerr := mainDB.Close(); cerr != nil {
			fmt.Fprintf(stderr, "[AUDIT] failed to close main database: %v\n", cerr)
		}
	}()

	order, queued, err := loadQueue(mainDB)
	if err != nil {
		fmt.Fprintf(stderr, "[AUDIT] failed to read post_queue: %v\n", err)
		return 2
	}
	if *verbose {
		fmt.Fprintf(stderr, "[AUDIT] %d group(s) with post_queue rows in %s\n", len(order), *dataDir)
	}

	var checked, flagged, missing, errs int
	for _, group := range order {
		rows := queued[group]
		path := groupDBPath(*dataDir, group)
		if _, serr := os.Stat(path); serr != nil {
			missing += len(rows)
			fmt.Fprintf(stderr, "[AUDIT] no group DB for %s (%s): %d queued message-id(s) skipped\n", group, path, len(rows))
			continue
		}
		c, f, m, gerr := auditGroup(path, rows, *all, *verbose, stdout, stderr)
		checked += c
		flagged += f
		missing += m
		if gerr != nil {
			errs++
			fmt.Fprintf(stderr, "[AUDIT] group %s (%s): %v\n", group, path, gerr)
		}
	}

	fmt.Fprintf(stdout, "checked=%d flagged=%d missing=%d\n", checked, flagged, missing)
	if errs > 0 {
		return 2
	}
	if flagged > 0 {
		return 1
	}
	return 0
}

// openReadOnly opens a SQLite file read-only. The DSN is the guarantee that
// nothing in this tool can write to it.
func openReadOnly(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		if cerr := db.Close(); cerr != nil {
			return nil, fmt.Errorf("ping: %w (close: %v)", err, cerr)
		}
		return nil, err
	}
	return db, nil
}

// groupDBPath is the on-disk location of a group database
// (see internal/database/db_groupdbs.go).
func groupDBPath(dataDir, group string) string {
	return filepath.Join(dataDir, "db", database.MD5Hash(group), database.SanitizeGroupName(group)+".db")
}

// loadQueue reads every post_queue row with its newsgroup name and groups the
// rows by newsgroup, keeping a stable group order.
func loadQueue(db *sql.DB) ([]string, map[string][]queueRow, error) {
	const query = `SELECT n.name, q.message_id, q.created, q.posted_to_remote
	               FROM post_queue q JOIN newsgroups n ON n.id = q.newsgroup_id
	               ORDER BY n.name, q.id`
	rows, err := db.Query(query)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var order []string
	queued := make(map[string][]queueRow)
	for rows.Next() {
		var name string
		var messageID, created, posted sql.NullString
		if err := rows.Scan(&name, &messageID, &created, &posted); err != nil {
			return nil, nil, err
		}
		if _, ok := queued[name]; !ok {
			order = append(order, name)
		}
		queued[name] = append(queued[name], queueRow{
			group:          name,
			messageID:      messageID.String,
			created:        created.String,
			postedToRemote: posted.String,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return order, queued, nil
}

// auditGroup inspects one group database read-only and reports its findings.
// It returns the number of articles checked, the number of flagged articles and
// the number of queued message-ids that have no article in the group database.
func auditGroup(path string, rows []queueRow, all, verbose bool, stdout, stderr io.Writer) (checked, flagged, missing int, err error) {
	db, err := openReadOnly(path)
	if err != nil {
		return 0, 0, len(rows), err
	}
	defer func() {
		if cerr := db.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close: %w", cerr)
		}
	}()

	byMessageID := make(map[string]queueRow, len(rows))
	for _, r := range rows {
		byMessageID[r.messageID] = r
	}

	report := func(q queueRow, a articleRow) {
		found := checkArticle(a)
		checked++
		if len(found) == 0 {
			return
		}
		flagged++
		for _, f := range found {
			fmt.Fprintf(stdout, "%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
				tsvSafe(q.group), a.articleNum, tsvSafe(a.messageID),
				tsvSafe(q.postedToRemote), tsvSafe(q.created), f.field, tsvSafe(f.reason))
		}
	}

	if all {
		group := ""
		if len(rows) > 0 {
			group = rows[0].group
		}
		seen := make(map[string]bool, len(rows))
		artRows, qerr := db.Query(selectArticleFields + " ORDER BY article_num")
		if qerr != nil {
			return checked, flagged, len(rows), qerr
		}
		defer artRows.Close()
		for artRows.Next() {
			a, serr := scanArticle(artRows)
			if serr != nil {
				return checked, flagged, missing, serr
			}
			seen[a.messageID] = true
			q, ok := byMessageID[a.messageID]
			if !ok {
				q = queueRow{group: group, postedToRemote: "-", created: "-"}
			}
			report(q, a)
		}
		if aerr := artRows.Err(); aerr != nil {
			return checked, flagged, missing, aerr
		}
		for _, r := range rows {
			if !seen[r.messageID] {
				missing++
				if verbose {
					fmt.Fprintf(stderr, "[AUDIT] %s: no article for queued message-id %s\n", r.group, tsvSafe(r.messageID))
				}
			}
		}
		return checked, flagged, missing, nil
	}

	stmt, perr := db.Prepare(selectArticleFields + " WHERE message_id = ?")
	if perr != nil {
		return checked, flagged, len(rows), perr
	}
	defer func() {
		if cerr := stmt.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close statement: %w", cerr)
		}
	}()
	for _, r := range rows {
		found, qerr := auditQueued(stmt, r, report)
		if qerr != nil {
			return checked, flagged, missing, qerr
		}
		if !found {
			missing++
			if verbose {
				fmt.Fprintf(stderr, "[AUDIT] %s: no article for queued message-id %s\n", r.group, tsvSafe(r.messageID))
			}
		}
	}
	return checked, flagged, missing, nil
}

// auditQueued reports the article of one queued message-id and says whether the
// group database holds it at all.
func auditQueued(stmt *sql.Stmt, r queueRow, report func(queueRow, articleRow)) (bool, error) {
	rows, err := stmt.Query(r.messageID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		a, serr := scanArticle(rows)
		if serr != nil {
			return found, serr
		}
		found = true
		report(r, a)
	}
	if err := rows.Err(); err != nil {
		return found, err
	}
	return found, nil
}

// scanArticle reads one article row; every text column may be NULL.
func scanArticle(rows *sql.Rows) (articleRow, error) {
	var a articleRow
	var messageID, subject, fromHeader, references, headersJSON sql.NullString
	if err := rows.Scan(&a.articleNum, &messageID, &subject, &fromHeader, &references, &headersJSON); err != nil {
		return a, err
	}
	a.messageID = messageID.String
	a.subject = subject.String
	a.fromHeader = fromHeader.String
	a.references = references.String
	a.headersJSON = headersJSON.String
	return a, nil
}

// checkArticle returns every reason the article looks like it carries injected
// header lines.
func checkArticle(a articleRow) []finding {
	var out []finding
	fields := []struct {
		name  string
		value string
		lfOK  bool // headers_json legitimately holds header lines joined by \n
	}{
		{"message_id", a.messageID, false},
		{"subject", a.subject, false},
		{"from_header", a.fromHeader, false},
		{"references", a.references, false},
		{"headers_json", a.headersJSON, true},
	}
	for _, f := range fields {
		if strings.Contains(f.value, "\x00") {
			out = append(out, finding{f.name, "contains a NUL byte"})
		}
		if strings.Contains(f.value, "\r") {
			out = append(out, finding{f.name, "contains a carriage return"})
		}
		if !f.lfOK && strings.Contains(f.value, "\n") {
			out = append(out, finding{f.name, "contains a line feed"})
		}
	}
	if !auditMessageIDRe.MatchString(a.messageID) {
		out = append(out, finding{"message_id", "does not match ^<[^<>\\s@]+@[^<>\\s@]+>$"})
	}
	out = append(out, checkHeaderLines(a.headersJSON)...)
	return out
}

// checkHeaderLines inspects headers_json, which a web post stores as its header
// lines joined by "\n"; a line starting with a space or tab is a continuation of
// the line before it.
func checkHeaderLines(headersJSON string) []finding {
	if headersJSON == "" {
		return nil
	}
	var out []finding
	seen := make(map[string]int)
	for i, line := range strings.Split(headersJSON, "\n") {
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if i == 0 {
				out = append(out, finding{"headers_json", "starts with a continuation line: " + shorten(line)})
			}
			continue
		}
		name, _, ok := strings.Cut(line, ":")
		if !ok {
			out = append(out, finding{"headers_json", "header line without a colon: " + shorten(line)})
			continue
		}
		lower := strings.ToLower(strings.TrimSpace(name))
		seen[lower]++
		if !allowedHeaderNames[lower] {
			out = append(out, finding{"headers_json", "unexpected header line: " + shorten(line)})
		}
		if singleHeaderNames[lower] && seen[lower] == 2 {
			out = append(out, finding{"headers_json", "duplicate header line: " + shorten(name)})
		}
	}
	return out
}

// shorten caps a value so one finding stays one readable line.
func shorten(s string) string {
	const max = 60
	s = tsvSafe(s)
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// tsvSafe keeps a value inside its TSV column: the bytes this tool hunts for
// must not break the output they are reported in.
var tsvReplacer = strings.NewReplacer("\t", `\t`, "\n", `\n`, "\r", `\r`, "\x00", `\0`)

func tsvSafe(s string) string {
	return tsvReplacer.Replace(s)
}
