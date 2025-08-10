# Database Migrations

This directory contains database migra### Main Database (`main.db`)
- `config` - System configuration
- `providers` - NNTP server configurations
- `newsgroups` - Subscribed newsgroups metadata (with expiry_days, max_articles)
- `users` - User accounts
- `sessions` - User sessions
- `user_permissions` - Access control
- `sections` - Newsgroup sections/categories
- `section_groups` - Section-to-newsgroup mappings

### Active Database (`active.db`)
- `newsgroups` - NNTP server compatibility registry

### Per-Group Databases (`group_<name>_*.db`)
- **Overview DB**: `overview` - XOVER data cache for performance
- **Articles DB**: `articles` - Full article storage with headers and body
- **Threads DB**: `threads` - Threading relationships and hierarchyo-pugleaf.

## Migration System

The migration system supports sequential, version-controlled schema changes across multiple database types:
- **Main Database** (`main.db`) - System configuration, providers, newsgroups metadata, users, sessions
- **Active Database** (`active.db`) - NNTP server compatibility layer for newsgroup registry
- **Per-Group Databases** - Overview, Articles, and Threads databases for each newsgroup

## Migration File Naming Convention

Migration files must follow the naming pattern: `XXXX_TYPE_DESCRIPTION.sql`

- `XXXX` - 4-digit version number (0001, 0002, etc.)
- `TYPE` - Database type identifier:
  - `main` - Main database migrations
  - `active` - Active database migrations
  - `overview` - Per-group overview database migrations
  - `articles` - Per-group articles database migrations
  - `threads` - Per-group threads database migrations
- `DESCRIPTION` - Brief description of what the migration does

### Examples:
- `0001_main_initial_schema.sql` - Initial main database schema
- `0002_main_newsgroups_expiry_fields.sql` - Add expiry fields to newsgroups
- `0001_overview_schema.sql` - Initial overview database schema
- `0002_overview_indexes.sql` - Add performance indexes to overview
- `0002_active_newsgroups_schema.sql` - Active database newsgroups table

## Migration Tracking

Each database type maintains its own `schema_migrations` table to track applied migrations:
- `filename` - The migration file name
- `db_type` - The database type (main, active, overview, articles, threads)
- `applied_at` - Timestamp when the migration was applied

## How Migrations Work

1. **Main Database**: Migrations are applied during `db.Migrate()` call at startup
2. **Active Database**: Migrations are applied during `db.Migrate()` call at startup
3. **Per-Group Databases**: Migrations are applied when each group database is first opened or via `db.MigrateGroup(groupName)`

## Migration Application

Migrations are applied in version order (0001, 0002, 0003, etc.) and only applied once per database. The system automatically:
- Creates the `schema_migrations` table if it doesn't exist
- Reads existing applied migrations from the tracking table
- Applies new migrations in sequential order
- Records each successful migration in the tracking table

## Usage

Migrations are applied automatically during:
- System startup (main and active databases)
- First access to a newsgroup (per-group databases)
- Manual migration via `db.MigrateGroup(groupName)`

## Database Schema Overview

### Main Database (`main.db`)
- `config` - System configuration
- `providers` - NNTP server configurations
- `newsgroups` - Subscribed newsgroups metadata
- `users` - User accounts
- `sessions` - User sessions
- `user_permissions` - Access control

### Per-Group Databases (`group_<name>.db`)
- `articles` - Full article storage
- `threads` - Threading relationships
- `overview` - XOVER data cache for performance

## Usage

Migrations will be applied automatically on startup