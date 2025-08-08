#!/bin/bash

# Build script for merge-descriptions tool

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

echo "Building merge-descriptions tool..."
echo "Project root: $PROJECT_ROOT"

cd "$PROJECT_ROOT"

# Build the tool
go build -o merge-descriptions ./cmd/merge-descriptions/

echo "Built merge-descriptions successfully"
echo "Usage: ./merge-descriptions <active_files_dir>"
echo "Example: ./merge-descriptions active_files/isc"
