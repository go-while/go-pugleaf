-- go-pugleaf: Single database per group schema
-- This file creates all tables in a single database per group
-- Created: 2025-01-02
-- Replaces the 3-database-per-group architecture
-- Based on EXACT original schemas from 0001_articles_schema.sql, 0001_overview_schema.sql, 0001_threads_schema.sql

-- Performance optimizations for batch processing
PRAGMA foreign_keys = ON;

PRAGMA synchronous = OFF;    -- Maximum speed, minimal safety for testing
PRAGMA journal_mode = DELETE; -- Back to rollback journal for testing
PRAGMA cache_size = -64000;  -- 64MB cache size
PRAGMA temp_store = MEMORY;  -- Use memory for temporary storage

-- Articles table: stores full article data
CREATE TABLE IF NOT EXISTS articles (
    article_num INTEGER PRIMARY KEY, -- NNTP article number in group
    message_id TEXT NOT NULL UNIQUE,
    subject TEXT,
    from_header TEXT,
    date_sent DATETIME,
    date_string TEXT,
    "references" TEXT,
    bytes INTEGER,
    lines INTEGER,
    reply_count INTEGER DEFAULT 0,
    path TEXT DEFAULT 'not-for-mail', -- NNTP Path header
    headers_json TEXT, -- All headers as JSON
    body_text TEXT,    -- Article body
    downloaded INTEGER NOT NULL DEFAULT 0,
    imported_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    spam INTEGER DEFAULT 0 NOT NULL, -- Spam flag counter
    hide INTEGER DEFAULT 0 NOT NULL  -- Hide flag counter
);

-- Threads table: stores parent/child relationships for threading
-- Performance optimization: No foreign keys for maximum batch insert speed
CREATE TABLE IF NOT EXISTS threads (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    root_article INTEGER NOT NULL,   -- article_num of thread root
    parent_article INTEGER,          -- article_num of parent (NULL for root)
    child_article INTEGER NOT NULL,  -- article_num of child
    depth INTEGER NOT NULL,
    thread_order INTEGER
    -- No foreign key constraints - application handles data integrity
);

-- Thread cache table: stores pre-computed thread data for fast web access
CREATE TABLE IF NOT EXISTS thread_cache (
    thread_root INTEGER NOT NULL PRIMARY KEY,  -- article_num of thread root
    root_date DATETIME,                         -- date of root article
    message_count INTEGER DEFAULT 1,            -- total messages in thread
    child_articles TEXT DEFAULT '',             -- "123,456,789" - comma-separated child article numbers
    last_child_number INTEGER,                  -- highest article number in thread (for cache invalidation)
    last_activity DATETIME,                     -- most recent activity in thread
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
    --FOREIGN KEY (thread_root) REFERENCES articles(article_num) ON DELETE CASCADE
);

-- Cached Trees table: stores pre-computed hierarchical tree structure
-- Performance optimization: No foreign keys for maximum batch insert speed
CREATE TABLE IF NOT EXISTS cached_trees (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    thread_root INTEGER NOT NULL,              -- article_num of thread root (references thread_cache.thread_root)
    article_num INTEGER NOT NULL,              -- article_num of this node in the tree
    parent_article INTEGER,                    -- article_num of immediate parent (NULL for root)
    depth INTEGER NOT NULL DEFAULT 0,          -- depth in tree (0=root, 1=direct reply, 2=nested reply, etc.)
    child_count INTEGER DEFAULT 0,             -- number of direct children this node has
    descendant_count INTEGER DEFAULT 0,        -- total descendants (children + grandchildren + etc.)
    tree_path TEXT NOT NULL,                   -- materialized path: "0.1.3.7" for traversal
    sort_order INTEGER,                        -- pre-computed sort order for tree display
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    -- Constraints
    UNIQUE(thread_root, article_num)
    -- No foreign key constraints - application handles data integrity
);

-- Tree statistics table: aggregated stats for each thread tree
CREATE TABLE IF NOT EXISTS tree_stats (
    thread_root INTEGER PRIMARY KEY,           -- article_num of thread root
    max_depth INTEGER DEFAULT 0,               -- deepest nesting level in this thread
    total_nodes INTEGER DEFAULT 1,             -- total articles in tree (including root)
    leaf_count INTEGER DEFAULT 0,              -- number of leaf nodes (articles with no replies)
    tree_structure TEXT,                       -- JSON representation of tree structure for quick loading
    last_updated DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (thread_root) REFERENCES thread_cache(thread_root) ON DELETE CASCADE
);

-- ========================================
-- INDEXES (from all original schema files)
-- ========================================

-- Articles indexes (from migrations/old/0001_articles_schema.sql)
CREATE INDEX IF NOT EXISTS idx_articles_message_id ON articles(message_id);
CREATE INDEX IF NOT EXISTS idx_articles_date_sent ON articles(date_sent);

-- Threads indexes (from migrations/old/0001_threads_schema.sql)
CREATE INDEX IF NOT EXISTS idx_threads_root ON threads(root_article);
CREATE INDEX IF NOT EXISTS idx_threads_parent ON threads(parent_article);
CREATE INDEX IF NOT EXISTS idx_threads_child ON threads(child_article);
CREATE INDEX IF NOT EXISTS idx_threads_depth ON threads(depth);
CREATE INDEX IF NOT EXISTS idx_threads_order ON threads(thread_order);

-- Thread cache indexes (from migrations/old/0001_threads_schema.sql)
CREATE INDEX IF NOT EXISTS idx_thread_cache_last_activity ON thread_cache(last_activity DESC);
CREATE INDEX IF NOT EXISTS idx_thread_cache_last_child ON thread_cache(last_child_number);
CREATE INDEX IF NOT EXISTS idx_thread_cache_message_count ON thread_cache(message_count DESC);

-- Cached trees indexes (from migrations/old/0001_threads_schema.sql)
CREATE INDEX IF NOT EXISTS idx_cached_trees_thread_root ON cached_trees(thread_root);
CREATE INDEX IF NOT EXISTS idx_cached_trees_parent ON cached_trees(parent_article);
CREATE INDEX IF NOT EXISTS idx_cached_trees_depth ON cached_trees(depth);
CREATE INDEX IF NOT EXISTS idx_cached_trees_sort_order ON cached_trees(thread_root, sort_order);
CREATE INDEX IF NOT EXISTS idx_cached_trees_path ON cached_trees(tree_path);
CREATE INDEX IF NOT EXISTS idx_cached_trees_updated ON cached_trees(updated_at);

-- Tree stats indexes (from migrations/old/0001_threads_schema.sql)
CREATE INDEX IF NOT EXISTS idx_tree_stats_max_depth ON tree_stats(max_depth);
CREATE INDEX IF NOT EXISTS idx_tree_stats_total_nodes ON tree_stats(total_nodes DESC);
CREATE INDEX IF NOT EXISTS idx_tree_stats_updated ON tree_stats(last_updated);

-- ========================================
-- TRIGGERS (from original schema files)
-- ========================================

-- Create trigger to maintain tree statistics (from migrations/old/0001_threads_schema.sql)
-- Performance optimization: Defer expensive aggregations
CREATE TRIGGER IF NOT EXISTS update_tree_stats_on_cached_trees_change
AFTER INSERT ON cached_trees
-- WHEN NEW.article_num = NEW.thread_root -- Only trigger for root articles to reduce overhead
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

-- Create indexes for performance on spam/hide queries
CREATE INDEX IF NOT EXISTS idx_articles_spam ON articles(spam);
CREATE INDEX IF NOT EXISTS idx_articles_hide ON articles(hide);

