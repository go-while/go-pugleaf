#!/bin/bash

# Extract newsgroups by hierarchy from active file
# Usage: ./extract_hierarchies.sh

ACTIVE_FILE="active.2025-06"
OUTPUT_DIR="hierarchies"

# Create output directory
mkdir -p "$OUTPUT_DIR"

# Array of supported hierarchies from the SQL schema
hierarchies=(
    # Primary Big 8 hierarchies
    "comp" "humanities" "misc" "news" "rec" "sci" "soc" "talk"

    # Alternative hierarchies
    "alt"

    # Regional/Language hierarchies
    "de" "fr" "es" "it" "nl" "uk" "au" "ca" "fi" "no" "se" "dk"
    "jp" "kr" "cn" "ru" "pl" "cz" "hu" "gr" "pt" "br"

    # Technical/Computing
    "linux" "unix" "microsoft" "apple" "android" "windows" "mac"
    "freebsd" "netbsd" "openbsd" "debian" "ubuntu" "fedora" "suse"
    "redhat" "gentoo" "slackware"

    # Programming languages
    "java" "python" "perl" "php" "javascript" "go" "rust"
    "haskell" "lisp" "ruby" "scala" "swift" "kotlin"

    # Internet/Network related
    "inet" "fido" "fidonet" "bitnet"

    # Educational institutions
    "mit" "stanford" "berkeley" "harvard" "caltech" "cmu" "cornell"

    # Special purpose
    "bionet" "k12" "biz" "clari" "clarinet"

    # Regional US hierarchies
    "ba" "boston" "chicago" "la" "nyc" "seattle" "austin" "phoenix"

    # Test and control
    "test" "control" "junk"
)

echo "Extracting newsgroups from $ACTIVE_FILE into hierarchy files..."

# Check if active file exists
if [ ! -f "$ACTIVE_FILE" ]; then
    echo "Error: Active file $ACTIVE_FILE not found!"
    exit 1
fi

total_groups=0
matched_groups=0

# Extract groups for each hierarchy
for hierarchy in "${hierarchies[@]}"; do
    echo -n "Processing $hierarchy... "

    # Extract groups that start with hierarchy. or are exactly the hierarchy name
    output_file="$OUTPUT_DIR/${hierarchy}.active"

    # Use grep to find matching lines, then sort them
    grep "^${hierarchy}\." "$ACTIVE_FILE" > "$output_file" 2>/dev/null
    grep "^${hierarchy} " "$ACTIVE_FILE" >> "$output_file" 2>/dev/null

    # Sort and remove duplicates
    if [ -s "$output_file" ]; then
        sort -u "$output_file" -o "$output_file"
        count=$(wc -l < "$output_file")
        echo "$count groups"
        matched_groups=$((matched_groups + count))
    else
        echo "0 groups"
        rm -f "$output_file"
    fi
done

# Create a file with all unmatched groups
echo -n "Creating unknown.active file... "
temp_matched="/tmp/matched_groups.tmp"
temp_all="/tmp/all_groups.tmp"

# Combine all matched groups
cat "$OUTPUT_DIR"/*.active > "$temp_matched" 2>/dev/null || touch "$temp_matched"
cp "$ACTIVE_FILE" "$temp_all"

# Find unmatched groups
comm -23 <(sort "$temp_all") <(sort "$temp_matched") > "$OUTPUT_DIR/unknown.active"

unknown_count=$(wc -l < "$OUTPUT_DIR/unknown.active")
echo "$unknown_count groups"

# Cleanup temp files
rm -f "$temp_matched" "$temp_all"

# Get total group count
total_groups=$(wc -l < "$ACTIVE_FILE")

echo ""
echo "Summary:"
echo "Total groups in active file: $total_groups"
echo "Groups matched to hierarchies: $matched_groups"
echo "Unmatched groups: $unknown_count"
echo ""
echo "Top 10 hierarchies by group count:"
echo "=================================="

# Show statistics for created files
for file in "$OUTPUT_DIR"/*.active; do
    if [ -f "$file" ]; then
        basename_file=$(basename "$file" .active)
        count=$(wc -l < "$file")
        echo "$basename_file $count"
    fi
done | sort -k2 -nr | head -10 | while read name count; do
    printf "%-15s: %6d groups\n" "$name" "$count"
done

echo ""
echo "Files created in: $OUTPUT_DIR/"
echo ""
echo "To view a specific hierarchy, use: cat $OUTPUT_DIR/<hierarchy>.active"
echo "Example: cat $OUTPUT_DIR/comp.active"
