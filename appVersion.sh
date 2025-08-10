#!/bin/bash

# buildNumber.sh - Increment version number with rollover logic
# Usage: ./buildNumber.sh

VERSION_FILE="appVersion.txt"

# Create version file if it doesn't exist
if [ ! -f "$VERSION_FILE" ]; then
    echo "0.0.0" > "$VERSION_FILE"
    echo "Created $VERSION_FILE with initial version 0.0.0"
fi

# Read current version
CURRENT_VERSION=$(cat "$VERSION_FILE")

# Validate version format (MAJOR.MINOR.PATCH)
if [[ ! $CURRENT_VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "Error: Invalid version format in $VERSION_FILE. Expected: MAJOR.MINOR.PATCH"
    echo "Current content: $CURRENT_VERSION"
    exit 1
fi

# Split version into components
IFS='.' read -r MAJOR MINOR PATCH <<< "$CURRENT_VERSION"

# Increment patch version
PATCH=$((PATCH + 1))

# Handle rollover: 9 -> 10 affects next level up
if [ $PATCH -eq 10 ]; then
    PATCH=0
    MINOR=$((MINOR + 1))

    if [ $MINOR -eq 10 ]; then
        MINOR=0
        MAJOR=$((MAJOR + 1))
    fi
fi

# Create new version string
NEW_VERSION="$MAJOR.$MINOR.$PATCH"

# Write new version to file
echo "$NEW_VERSION" > "$VERSION_FILE"

# Display result
echo "Version incremented: $CURRENT_VERSION → $NEW_VERSION"
