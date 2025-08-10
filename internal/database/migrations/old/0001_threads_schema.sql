-- go-pugleaf: Per-group threads database schema
-- This file should be applied to each group_<name>_threads.db
-- Created: 2025-06-20

PRAGMA foreign_keys = ON;

-- Threads table: stores parent/child relationships for threading
CREATE TABLE IF NOT EXISTS threads (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    root_article INTEGER NOT NULL,   -- article_num of thread root
    parent_article INTEGER,          -- article_num of parent (NULL for root)
    child_article INTEGER NOT NULL,  -- article_num of child
    depth INTEGER NOT NULL,
    thread_order INTEGER
    -- Note: Foreign keys removed because articles table is in separate database file
);

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_threads_root ON threads(root_article);
CREATE INDEX IF NOT EXISTS idx_threads_parent ON threads(parent_article);
CREATE INDEX IF NOT EXISTS idx_threads_child ON threads(child_article);
CREATE INDEX IF NOT EXISTS idx_threads_depth ON threads(depth);
CREATE INDEX IF NOT EXISTS idx_threads_order ON threads(thread_order);

-- Thread cache table: stores pre-computed thread data for fast web access
CREATE TABLE IF NOT EXISTS thread_cache (
    thread_root INTEGER NOT NULL PRIMARY KEY,  -- article_num of thread root
    root_date DATETIME,                         -- date of root article
    message_count INTEGER DEFAULT 1,            -- total messages in thread
    child_articles TEXT DEFAULT '',             -- "123,456,789" - comma-separated child article numbers
    last_child_number INTEGER,                  -- highest article number in thread (for cache invalidation)
    last_activity DATETIME,                     -- most recent activity in thread
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Indexes for thread_cache performance
CREATE INDEX IF NOT EXISTS idx_thread_cache_last_activity ON thread_cache(last_activity DESC);
CREATE INDEX IF NOT EXISTS idx_thread_cache_last_child ON thread_cache(last_child_number);
CREATE INDEX IF NOT EXISTS idx_thread_cache_message_count ON thread_cache(message_count DESC);

-- New migration: Add cached_trees table for hierarchical thread tree caching
-- This builds on the existing thread_cache system by adding tree structure data
-- File: migrations/0002_group_threads_tree_cache.sql

PRAGMA foreign_keys = ON;

-- Cached Trees table: stores pre-computed hierarchical tree structure
CREATE TABLE IF NOT EXISTS cached_trees (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    thread_root INTEGER NOT NULL,              -- article_num of thread root (references thread_cache.thread_root)
    article_num INTEGER NOT NULL,              -- article_num of this node in the tree
    parent_article INTEGER,                    -- article_num of immediate parent (NULL for root)
    depth INTEGER NOT NULL DEFAULT 0,          -- depth in tree (0=root, 1=direct reply, 2=nested reply, etc.)
    child_count INTEGER DEFAULT 0,             -- number of direct children this node has
    descendant_count INTEGER DEFAULT 0,        -- total descendants (children + grandchildren + etc.)
    tree_path TEXT,                            -- materialized path: "0.1.3.7" for traversal
    sort_order INTEGER,                        -- pre-computed sort order for tree display
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,

    -- Constraints
    UNIQUE(thread_root, article_num)
);

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_cached_trees_thread_root ON cached_trees(thread_root);
CREATE INDEX IF NOT EXISTS idx_cached_trees_parent ON cached_trees(parent_article);
CREATE INDEX IF NOT EXISTS idx_cached_trees_depth ON cached_trees(depth);
CREATE INDEX IF NOT EXISTS idx_cached_trees_sort_order ON cached_trees(thread_root, sort_order);
CREATE INDEX IF NOT EXISTS idx_cached_trees_path ON cached_trees(tree_path);
CREATE INDEX IF NOT EXISTS idx_cached_trees_updated ON cached_trees(updated_at);

-- Tree statistics table: aggregated stats for each thread tree
CREATE TABLE IF NOT EXISTS tree_stats (
    thread_root INTEGER PRIMARY KEY,           -- article_num of thread root
    max_depth INTEGER DEFAULT 0,               -- deepest nesting level in this thread
    total_nodes INTEGER DEFAULT 1,             -- total articles in tree (including root)
    leaf_count INTEGER DEFAULT 0,              -- number of leaf nodes (articles with no replies)
    tree_structure TEXT,                       -- JSON representation of tree structure for quick loading
    last_updated DATETIME DEFAULT CURRENT_TIMESTAMP,

    -- Foreign key relationship to thread_cache
    FOREIGN KEY (thread_root) REFERENCES thread_cache(thread_root) ON DELETE CASCADE
);

-- Indexes for tree_stats
CREATE INDEX IF NOT EXISTS idx_tree_stats_max_depth ON tree_stats(max_depth);
CREATE INDEX IF NOT EXISTS idx_tree_stats_total_nodes ON tree_stats(total_nodes DESC);
CREATE INDEX IF NOT EXISTS idx_tree_stats_updated ON tree_stats(last_updated);

-- Create trigger to maintain tree statistics
CREATE TRIGGER IF NOT EXISTS update_tree_stats_on_cached_trees_change
AFTER INSERT ON cached_trees
BEGIN
    INSERT OR REPLACE INTO tree_stats (
        thread_root,
        max_depth,
        total_nodes,
        leaf_count,
        last_updated
    )
    SELECT
        NEW.thread_root,
        MAX(depth) as max_depth,
        COUNT(*) as total_nodes,
        SUM(CASE WHEN child_count = 0 THEN 1 ELSE 0 END) as leaf_count,
        CURRENT_TIMESTAMP
    FROM cached_trees
    WHERE thread_root = NEW.thread_root;
END;
