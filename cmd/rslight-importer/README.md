# Legacy SQLite Database Importer

This tool imports legacy SQLite databases from RockSolid Light into the go-pugleaf database format.

## Features

- Imports the RockSolid Light sections and their newsgroups from the config directory (`-etc`)
- Imports articles from the legacy per-group SQLite databases (`*-articles.db3` in `-spool`)
- Processes every article like a fetched one (message-id history, overview, threading)
- Creates missing newsgroups (active) for every imported group
- Optional: resets all newsgroup data before a fresh import (`-YESresetallgroupsYES`)

## Building

```bash
./build_rslight_importer.sh       # writes build/rslight-importer, with the version from appVersion.txt
# or
go build -o build/rslight-importer ./cmd/rslight-importer
```

## Usage

`-etc` is required for every run (including a reset). Each run first imports the sections from
`-etc`; with `-spool` it then imports the articles.

### Sections and Groups Only

```bash
./build/rslight-importer -etc /etc/rslight -data ./data -nntphostname news.example.org
```

### Full Import

```bash
./build/rslight-importer \
  -etc /etc/rslight \
  -spool /var/spool/rslight \
  -data ./data \
  -nntphostname news.example.org \
  -threads 4
```

Before the articles are imported the tool waits 15 seconds: press Ctrl+C then if you only wanted
the sections and newsgroups. Ctrl+C (or SIGTERM) during the wait or the import stops it: no new
file is started, running files stop after their current article, the processor (history) and the
database are shut down cleanly and the tool exits with code 130. A failed import names the first
five failed files and counts the rest (every failure is also logged as it happens).

### Reset All Newsgroup Data

```bash
./build/rslight-importer -etc /etc/rslight -data ./data -YESresetallgroupsYES
```

⚠️ This permanently deletes ALL articles, threads, overview and cache data of ALL newsgroups and
resets all message counters to 0. It runs after the sections import, after a 5 second countdown
(Ctrl+C cancels: nothing is reset, exit code 130), and the run then exits without importing `-spool`. Delete the `<data>/history`
folder manually.

## Command Line Options

- `-etc`: Path to the legacy RockSolid Light config directory, e.g. `/etc/rslight` - **Required**, no default
- `-spool`: Path to the legacy RockSolid spool directory with the `*-articles.db3` files, e.g.
  `/var/spool/rslight` (no default; empty: only the sections are imported)
- `-data`: Directory to store the new go-pugleaf database files (default: `./data`)
- `-threads`: Number of `*-articles.db3` files imported in parallel (default: 1)
- `-nntphostname`: Your NNTP hostname; required on the first run, later runs fall back to the
  value stored in the database
- `-useshorthashlen`: Legacy short hash length (2-7, default: 7). The value of the first run is
  stored in the database; later runs use the stored value and warn if it differs. It has no effect
  on the history index, which stores full message-ids since Nov 2025
- `-history`: Maintain and use the message-id history index in `<data>/history` (default: true)
- `-pprof`: Serve net/http/pprof on a loopback address with a port, e.g. `localhost:6060` (default: off)
- `-YESresetallgroupsYES`: Reset all newsgroup data (see above)

Without `-etc` (or with an empty `-data`) the tool prints its usage and exits with status 1.
`-h` lists every flag with its default.

## Legacy Input

### Config Directory (`-etc`)

- `<etc>/menu.conf`: one section per line, `name:show_in_header:enable_local_spool`
  (e.g. `rocksolid:true:false`). Empty lines and `#` comments are ignored; the `spoolnews` entry is skipped.
- `<etc>/<section>/groups.txt`: one newsgroup per line, `group.name Description`. Lines starting
  with `:` are category headers. A section without `groups.txt` is skipped.

Sections and section groups that already exist are kept, so the sections import can run again.

### Spool Directory (`-spool`)

Only files named `<newsgroup>-articles.db3` are imported; other files in the directory are ignored.
The importer reads this table:

```sql
CREATE TABLE articles(
     id INTEGER PRIMARY KEY,
     newsgroup TEXT,
     number TEXT UNIQUE,
     msgid TEXT UNIQUE,
     date TEXT,
     name TEXT,
     subject TEXT,
     search_snippet TEXT,
     article TEXT
);
```

Only the columns `newsgroup`, `msgid` and `article` are read, in `id` order. `article` holds the
complete article in wire format (headers, body and the final `.` line); it is parsed and stored
like an article fetched from a server. The other columns and the `*-data.db3` (thread) files are
not used: threads are rebuilt from the article headers.

The newsgroup of an article is the one in the file name: when the `newsgroup` column differs
(a crosspost), the article is imported into the group of the file.

## Example Output

The log of a full import looks like this (abridged):

```
Starting go-pugleaf RSLIGHT-IMPORT (version: 4.7.5)
internal/legacy/main.go: Starting legacy section import...
internal/legacy/main.go: Found 3 sections in menu.conf
Importing section: rocksolid
internal/legacy/main.go: Imported 12 groups for section rocksolid
internal/legacy/main.go: Legacy section import completed!
Sections import completed!

=== Imported Sections Summary ===
Section: rocksolid    | Display: rocksolid       | Header: Yes | Groups: 12

=== Main Database Summary ===
Total newsgroups in main database: 12
Starting SQLite import from: /var/spool/rslight
Target database directory: ./data
[RSLIGHT-IMPORT] Starting from dir: '/var/spool/rslight'
[RSLIGHT-IMPORT] Found 2 SQLite database files
[RSLIGHT-IMPORT] Validated 2 SQLite database files for import
[RSLIGHT-IMPORT] Sleeping for 15s before import. You can cancel here with Ctrl+C if you only wanted to get the newsgroups added.
[RSLIGHT-IMPORT] Starting import of 2 SQLite database files with 1 threads
[RSLIGHT-IMPORT] DONE: 2541 imported, 0 failed, 0 skippedEmpty, 0 duplicates @db='/var/spool/rslight/comp.programming-articles.db3' took 5s
[RSLIGHT-IMPORT] internal wg.Wait() released, 0 of 2 files failed
[RSLIGHT-IMPORT] Total Imported: 3012 articles, Failed: 0 articles, Empty: 0, Duplicates: 0, GroupsDone: 2, FailedFiles: 0, SkippedFiles: 0  / 2
[RSLIGHT-IMPORT] SUCCESS! Import process completed! Shutting down systems...
RSLIGHT import completed successfully!
```

## Error Handling

- Articles that cannot be parsed or processed are counted as failed and logged; the import of the
  file continues. Empty articles and articles already known to the history are counted separately.
- A file that cannot be read is logged and counted in `FailedFiles`; the other files are still imported.
- Files with suspicious names, an empty group name or an unreadable `articles` table are skipped.
- The run exits with status 1 when a step fails, when any file failed, or when every file was skipped.

## Dependencies

- Go (see `go.mod` for the version)
- github.com/mattn/go-sqlite3
- go-pugleaf internal packages
