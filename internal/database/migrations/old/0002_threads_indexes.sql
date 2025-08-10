-- Migration for improved threads performance
-- Add indexes for common queries

CREATE INDEX IF NOT EXISTS idx_threads_root_article ON threads(root_article);
CREATE INDEX IF NOT EXISTS idx_threads_parent_article ON threads(parent_article);
CREATE INDEX IF NOT EXISTS idx_threads_child_article ON threads(child_article);
CREATE INDEX IF NOT EXISTS idx_threads_thread_order ON threads(thread_order);
