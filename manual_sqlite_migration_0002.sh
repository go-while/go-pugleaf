#!/bin/bash

# Apply NNNN_single_*.sql to all group databases

#MIGRATION_FILE="migrations/0002_single_spam_system.sql"
MIGRATION_FILE="migrations/0008_single_spam_hide_performance_indexes.sql"
DATA_PATH="data/db"

# Check if migration file exists
if [ ! -f "$MIGRATION_FILE" ]; then
    echo "Error: Migration file '$MIGRATION_FILE' not found!"
    exit 1
fi

MIGRATION_SQL=$(cat "$MIGRATION_FILE" | grep -v '^--' | grep -v '^$')

# Apply migration to a database
apply_migration() {
    local db_file="$1"

    # Check if articles table exists
    if ! sqlite3 "$db_file" "SELECT name FROM sqlite_master WHERE type='table' AND name='articles';" | grep -q "articles"; then
        echo "SKIP: $db_file (no articles table)"
        return 0
    fi

    # Check if spam column already exists
    if sqlite3 "$db_file" "PRAGMA table_info(articles);" | grep -q "spam"; then
        echo "SKIP: $db_file (already migrated)"
        return 0
    fi

    # Apply migration
    if sqlite3 "$db_file" "$MIGRATION_SQL"; then
        echo "OK: $db_file"
        return 0
    else
        echo "FAIL: $db_file - $(sqlite3 "$db_file" "$MIGRATION_SQL" 2>&1 | head -1)"
        return 1
    fi
}

# Process all .sq3 files
find "$DATA_PATH" -name "*.db" -type f | while read db_file; do
    apply_migration "$db_file"
done

echo "Done. Remove migration file: mv $MIGRATION_FILE ${MIGRATION_FILE}.applied"
