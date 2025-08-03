#!/bin/bash

# Organize hierarchy files by theme based on the SQL schema structure
# This script creates themed directories and organizes .active files accordingly

THEMED_DIR="themed"
SQL_FILE="../../migrations/0003_main_create_hierarchies.sql"

echo "Organizing hierarchy files by theme..."

# Create themed directory
mkdir -p "$THEMED_DIR"

# Function to create directory and move files
organize_category() {
    local category_name="$1"
    local category_desc="$2"
    shift 2
    local hierarchies=("$@")

    if [ ${#hierarchies[@]} -eq 0 ]; then
        return
    fi

    # Create category directory
    local cat_dir="$THEMED_DIR/$category_name"
    mkdir -p "$cat_dir"

    # Create README for category
    local readme="$cat_dir/README.md"
    echo "# $category_desc" > "$readme"
    echo "" >> "$readme"
    echo "This directory contains newsgroup hierarchies for: $category_desc" >> "$readme"
    echo "" >> "$readme"
    echo "## Hierarchies in this category:" >> "$readme"
    echo "" >> "$readme"

    local moved_count=0

    # Copy hierarchy files to category directory
    for hierarchy in "${hierarchies[@]}"; do
        local source_file="${hierarchy}.active"
        if [ -f "$source_file" ]; then
            local target_file="$cat_dir/${hierarchy}.active"
            cp "$source_file" "$target_file"

            # Count lines and add to README
            local line_count=$(wc -l < "$target_file" 2>/dev/null || echo "0")
            echo "- \`${hierarchy}.active\` - $hierarchy ($line_count groups)" >> "$readme"
            ((moved_count++))
        fi
    done

    if [ $moved_count -gt 0 ]; then
        echo "Created category '$category_name' with $moved_count hierarchies"
    else
        # Remove empty directory
        rm -rf "$cat_dir"
    fi
}

# Primary Big 8 hierarchies
organize_category "big8" "Primary Big 8 Hierarchies" \
    "comp" "humanities" "misc" "news" "rec" "sci" "soc" "talk"

# Alternative hierarchies
organize_category "alternative" "Alternative Hierarchies" \
    "alt"

# Regional/Language hierarchies
organize_category "regional_language" "Regional/Language Hierarchies" \
    "ar" "at" "au" "aus" "be" "bg" "br" "ca" "can" "ch" "cl" "cn" "co" "cr" \
    "cz" "de" "dk" "ee" "eg" "es" "fi" "fj" "fr" "gr" "hk" "hr" "hu" "id" \
    "ie" "il" "in" "is" "it" "jp" "kr" "lt" "lu" "lv" "mx" "my" "nl" "no" \
    "nz" "pe" "ph" "pk" "pl" "pt" "ro" "ru" "se" "sg" "si" "sk" "th" "tn" \
    "tr" "tw" "ua" "uk" "uy" "ve" "vn" "yu" "za"

# Regional US hierarchies
organize_category "regional_us" "Regional US Hierarchies" \
    "austin" "az" "ba" "boston" "chi" "co" "ct" "dc" "fl" "ga" "hawaii" \
    "houston" "ia" "il" "in" "ks" "ky" "la" "ma" "md" "me" "mi" "mn" "mo" \
    "ms" "mt" "nc" "nd" "ne" "nh" "nj" "nm" "nv" "ny" "nyc" "oh" "ok" "or" \
    "pa" "pdx" "phila" "phoenix" "pgh" "ri" "sc" "sd" "seattle" "stl" "tenn" \
    "triangle" "tx" "ut" "va" "vt" "wa" "wi" "wv" "wy"

# Technical/Computing
organize_category "technical_computing" "Technical/Computing" \
    "amiga" "android" "apple" "atari" "beos" "borland" "bsd" "commodore" \
    "debian" "dos" "fedora" "freebsd" "gentoo" "gnu" "grc" "ibm" "linux" \
    "mac" "mandriva" "microsoft" "msdos" "netbsd" "novell" "openbsd" "oracle" \
    "os2" "redhat" "sco" "sgi" "slackware" "solaris" "sun" "suse" "ubuntu" \
    "unix" "vms" "windows" "xenix"

# Programming languages
organize_category "programming" "Programming Languages" \
    "ada" "asm" "basic" "c" "c++" "clojure" "cobol" "dart" "delphi" "elixir" \
    "erlang" "forth" "fortran" "go" "groovy" "haskell" "java" "javascript" \
    "julia" "kotlin" "lisp" "lua" "matlab" "ml" "modula" "nim" "objective-c" \
    "ocaml" "pascal" "perl" "php" "prolog" "python" "r" "racket" "ruby" \
    "rust" "scala" "scheme" "smalltalk" "swift" "tcl" "typescript" "vb" \
    "verilog" "vhdl" "zig"

# Educational institutions
organize_category "educational" "Educational Institutions" \
    "berkeley" "caltech" "cmu" "columbia" "cornell" "dartmouth" "duke" \
    "gatech" "harvard" "indiana" "jhu" "mit" "msu" "ncsu" "northwestern" \
    "nyu" "osu" "princeton" "psu" "purdue" "rice" "rutgers" "stanford" \
    "tamu" "ucb" "ucla" "ucsb" "ucsd" "ufl" "uiuc" "umich" "umn" "unc" \
    "upenn" "usc" "utexas" "uw" "virginia" "vt" "wisc" "wustl" "yale"

# Hobby/Interest groups
organize_category "hobbies_interests" "Hobby/Interest Groups" \
    "aquaria" "astro" "auto" "aviation" "bicycles" "birds" "boats" "cats" \
    "climbing" "collecting" "comics" "crafts" "dance" "dogs" "electronics" \
    "equestrian" "film" "fitness" "food" "games" "gardening" "genealogy" \
    "guns" "ham" "history" "hunting" "martial-arts" "military" "models" \
    "motorcycles" "mudding" "music" "outdoors" "pets" "philosophy" "photo" \
    "politics" "puzzles" "radio" "railroads" "religion" "roguelike" "running" \
    "scouting" "scuba" "skiing" "space" "sport" "stamps" "startrek" "starwars" \
    "travel" "tv" "video" "wine" "woodworking"

# Corporate/Commercial
organize_category "corporate" "Corporate/Commercial" \
    "adobe" "amazon" "ap" "autodesk" "bbc" "biz" "clari" "clarinet" "corel" \
    "courts" "ebay" "google" "govnews" "intel" "lotus" "market" "mozilla" \
    "netscape" "nokia" "reuters" "sybase" "symantec" "vmware" "wordperfect" "xerox"

# Network related
organize_category "network" "Internet/Network Related" \
    "aol" "compuserve" "fidonet" "inet" "isp" "prodigy" "uunet" "well" \
    "bit" "bitnet" "fido" "fido7" "gateway" "lists"

# Special purpose
organize_category "special_purpose" "Special Purpose" \
    "answers" "bionet" "control" "eunet" "example" "free" "han" "info" \
    "israel" "japan" "junk" "k12" "local" "nzl" "relcom" "school" "test" \
    "to" "tnn" "usenet" "vmsnet" "world"

# Academic/Research
organize_category "academic_research" "Academic/Research" \
    "aaai" "acm" "bioinformatics" "biomed" "chem" "cognitive" "comp-sci" \
    "ecology" "eng" "genetics" "geology" "ieee" "linguistics" "math" \
    "meteorology" "neuroscience" "physics" "psychology" "robotics" \
    "sociology" "statistics"

# Create master index
echo "Creating master index..."
cat > "$THEMED_DIR/INDEX.md" << 'EOF'
# Usenet Hierarchy Categories

This directory contains newsgroup hierarchies organized by theme/category.

## Categories:

EOF

# List directories and count files
total_categories=0
total_files=0

for dir in "$THEMED_DIR"/*/; do
    if [ -d "$dir" ]; then
        dir_name=$(basename "$dir")
        file_count=$(find "$dir" -name "*.active" | wc -l)
        if [ $file_count -gt 0 ]; then
            echo "- [\`$dir_name/\`](./$dir_name/) - $file_count hierarchies" >> "$THEMED_DIR/INDEX.md"
            ((total_categories++))
            ((total_files += file_count))
        fi
    fi
done

cat >> "$THEMED_DIR/INDEX.md" << EOF

## Statistics:

- Total categories: $total_categories
- Total hierarchy files: $total_files
EOF

echo ""
echo "Summary:"
echo "Created $total_categories themed categories"
echo "Organized $total_files hierarchy files"
echo "Files organized in: $THEMED_DIR/"
echo "See $THEMED_DIR/INDEX.md for an overview"
