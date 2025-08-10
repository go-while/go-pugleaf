-- go-pugleaf: Add indexes for sorting functionality in hierarchies and newsgroups

-- Add index on hierarchies.last_updated for sorting by newest activity
-- This optimizes the "newest" sort option in hierarchies page
CREATE INDEX IF NOT EXISTS idx_hierarchies_last_updated ON hierarchies(last_updated);

-- Add index on newsgroups.updated_at for sorting by activity
-- This optimizes the "activity" sort option in hierarchy groups page
CREATE INDEX IF NOT EXISTS idx_newsgroups_updated_at ON newsgroups(updated_at);
