-- Optimize hierarchy queries by adding hierarchy column to newsgroups table
-- This eliminates expensive LIKE pattern matching for 42k+ newsgroups

-- Add hierarchy column to newsgroups table
ALTER TABLE newsgroups ADD COLUMN hierarchy TEXT;

-- Create index for fast hierarchy-based queries
CREATE INDEX IF NOT EXISTS idx_newsgroups_hierarchy ON newsgroups(hierarchy);

-- Create composite index for hierarchy + message_count filtering
CREATE INDEX IF NOT EXISTS idx_newsgroups_hierarchy_message_count ON newsgroups(hierarchy, message_count);

-- Populate the hierarchy column for existing newsgroups
-- Extract hierarchy from newsgroup name (everything before first dot)
UPDATE newsgroups
SET hierarchy = CASE
    WHEN name LIKE '%.%' THEN SUBSTR(name, 1, INSTR(name, '.') - 1)
    ELSE name
END
WHERE hierarchy IS NULL;

-- Update hierarchies group_count using the new hierarchy column (much faster)
UPDATE hierarchies
SET group_count = (
    SELECT COUNT(*)
    FROM newsgroups
    WHERE newsgroups.hierarchy = hierarchies.name
    AND newsgroups.active = 1
);
