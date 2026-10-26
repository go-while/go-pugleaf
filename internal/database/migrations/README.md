# Database Migrations

This directory contains database migrations for go-pugleaf.

## Migration System

The migration system supports sequential, version-controlled schema changes across two database kinds:
- **Main Database** (`<data>/cfg/pugleaf.sq3`) - System configuration, providers, newsgroups metadata, users, sessions
- **Per-Group Databases** (`<data>/db/<hash>/<group>.db`) - one SQLite file per newsgroup with articles, threads and the tree caches

The code (`internal/database/db_migrate.go`) also knows a type `active` (`MigrationTypeActive`), but there is no
active database and no migration of that type: such files would never be applied.

The `*.sql` files of this directory are compiled into the binaries (`//go:embed migrations/*.sql` in
`internal/database/embedded_migrations.go`). Only when the embedded set is empty does the code fall back to
reading `./migrations` relative to the working directory. Subdirectories (`old/`, `new/`) and files that do
not end in `.sql` (e.g. `0002_single_spam_system.sql.applied`) are not embedded and never applied.

## Migration File Naming Convention

Migration files must follow the naming pattern: `XXXX_TYPE_DESCRIPTION.sql`

- `XXXX` - 4-digit version number (0001, 0002, etc.)
- `TYPE` - Database type identifier:
  - `main` - Main database migrations
  - `single` - Per-group database migrations (the embedded loader also accepts `group`)
  - `active` - parsed, but never applied (see above)
- `DESCRIPTION` - Brief description of what the migration does

A file with another type is skipped with a warning. Version numbers are not unique: main and group
migrations share the numbering, and some numbers carry two main files (0002, 0007).

### Examples:
- `0001_main_schema.sql` - Initial main database schema
- `0002_main_newsgroups_expiry_fields.sql` - Add expiry fields to newsgroups
- `0001_single_db_schema.sql` - Initial per-group database schema
- `0010_single_article_num_seq.sql` - Monotonic article numbers per group (`article_num_seq`)
- `0031_main_nntp_users_unique_web.sql` - Latest main migration: one active NNTP account per web user

## Migration Tracking

Each database maintains its own `schema_migrations` table to track applied migrations:
- `id` - Row id
- `filename` - The migration file name (unique; this is what "applied" is keyed on)
- `db_type` - The database type (`main` or `group`; rows with an empty `db_type` count as applied for both)
- `applied_at` - Timestamp when the migration was applied

## How Migrations Work

1. **Main Database**: Migrations are applied by `db.Migrate()` (main database only), called by
   `database.OpenDatabase` at startup and again by `processor.NewProcessorE` (`NewProcessor`)
2. **Per-Group Databases**: Migrations are applied by `migrateGroupDB` every time a group database is opened
   (`db_groupdbs.go`); it reads that file's `schema_migrations` on each open, there is no per-process cache.
   There is no `MigrateGroup` function.

## Migration Application

Migrations are applied in version order (0001, 0002, 0003, etc.) and only applied once per database. Files with
the same version number have no defined order between them. The system automatically:
- Creates the `schema_migrations` table if it doesn't exist
- Reads existing applied migrations from the tracking table
- Applies new migrations in sequential order, each in one transaction together with its tracking row, on one
  connection with `foreign_keys` switched off during the migration and restored afterwards
- Strips whole-line `PRAGMA ...;` statements from the migration SQL (connection pragmas come from the driver)
- Logs (but does not fail on) rows reported by `PRAGMA foreign_key_check` after the commit
- Records each successful migration in the tracking table; a failing migration is rolled back and the
  error is returned, so opening the database fails

## Usage

Migrations are applied automatically during:
- System startup (main database)
- Every open of a newsgroup database (per-group databases)

## Database Schema Overview

### Main Database (`cfg/pugleaf.sq3`)
- `config` - System configuration
- `providers` - NNTP server configurations
- `newsgroups` - Subscribed newsgroups metadata (with expiry_days, max_articles)
- `users` - User accounts
- `sessions` - User sessions
- `user_permissions` - Access control
- `sections`, `section_groups` - Newsgroup sections/categories and their mappings
- `api_tokens`, `nntp_users`, `nntp_sessions`, `ai_models` - API tokens, NNTP accounts, AI chat models
- `hierarchies`, `site_news`, `spam`, `user_spam_flags`, `post_queue`, `cron_jobs` - added by later migrations

### Per-Group Databases (`db/<hash>/<group>.db`)
- `articles` - Full article storage; the overview (XOVER) fields are columns of this table, there is no
  separate overview table
- `threads` - Threading relationships
- `thread_cache`, `cached_trees`, `tree_stats` - Thread list and tree view caches
- `article_num_seq` - Highest article number ever assigned (migration 0010)
