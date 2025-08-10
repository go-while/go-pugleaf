#!/bin/bash

# Test script for the flat file import tool

set -e

echo "Setting up test data..."

# Create test directories
TEST_HEAD_DIR="/tmp/test_import/head"
TEST_BODY_DIR="/tmp/test_import/body"
TEST_DB_DIR="/tmp/test_import/db"

rm -rf /tmp/test_import
mkdir -p "$TEST_HEAD_DIR/0/0/0"
mkdir -p "$TEST_HEAD_DIR/a/b/c"
mkdir -p "$TEST_BODY_DIR/0/0/0"
mkdir -p "$TEST_BODY_DIR/a/b/c"
mkdir -p "$TEST_DB_DIR"

# Create test head files
cat > "$TEST_HEAD_DIR/0/0/0/123456789abcdef.head" << 'EOF'
Message-ID: <test1@example.com>
From: test@example.com
Subject: Test Article 1
Date: Mon, 1 Jan 2024 12:00:00 +0000

EOF

cat > "$TEST_HEAD_DIR/a/b/c/def456789123abc.head" << 'EOF'
Message-ID: <test2@example.com>
From: test2@example.com
Subject: Test Article 2 with ümläüts
Date: Mon, 1 Jan 2024 13:00:00 +0000

EOF

# Create corresponding body files
cat > "$TEST_BODY_DIR/0/0/0/123456789abcdef.body" << 'EOF'
This is the body of test article 1.
It has multiple lines.
End of article.
EOF

cat > "$TEST_BODY_DIR/a/b/c/def456789123abc.body" << 'EOF'
This is the body of test article 2.
It contains German umlauts: äöü ÄÖÜ ß
Multiple lines with special characters.
Grüße aus München!
EOF

echo "Test data created successfully."
echo "Head files: $(find $TEST_HEAD_DIR -name '*.head' | wc -l)"
echo "Body files: $(find $TEST_BODY_DIR -name '*.body' | wc -l)"

echo ""
echo "Running dry-run test..."
go run cmd/import-flat-files/main.go \
    -head "$TEST_HEAD_DIR" \
    -body "$TEST_BODY_DIR" \
    -db "$TEST_DB_DIR" \
    -workers 2 \
    -dry-run \
    -verbose

echo ""
echo "Running actual import test..."
go run cmd/import-flat-files/main.go \
    -head "$TEST_HEAD_DIR" \
    -body "$TEST_BODY_DIR" \
    -db "$TEST_DB_DIR" \
    -workers 2 \
    -verbose

echo ""
echo "Checking results..."

# Check if databases were created
echo "Databases created:"
ls -la "$TEST_DB_DIR"/*.db 2>/dev/null || echo "No databases found!"

# Check database contents
for db in "$TEST_DB_DIR"/*.db; do
    if [ -f "$db" ]; then
        echo ""
        echo "Checking database: $(basename $db)"
        sqlite3 "$db" "SELECT COUNT(*) as total_articles FROM articles;"
        sqlite3 "$db" "SELECT messageid_hash, substr(head, 1, 50) || '...' as head_preview FROM articles LIMIT 5;"
    fi
done

echo ""
echo "Test completed. Check output above for any errors."

# Cleanup
echo "Cleaning up test data..."
rm -rf /tmp/test_import

echo "Test finished successfully!"
