-- Fix thread_cache last_activity timestamps
-- This script recalculates last_activity based on the most recent non-hidden article in each thread

-- First, let's see what we're dealing with
.mode column
.headers on

-- Show threads with potentially problematic last_activity values
SELECT 
    thread_root,
    datetime(last_activity) as last_activity,
    CASE 
        WHEN last_activity > datetime('now', '+1 hour') THEN 'FUTURE'
        WHEN last_activity IS NULL THEN 'NULL'
        ELSE 'OK'
    END as status,
    message_count
FROM thread_cache 
WHERE last_activity > datetime('now', '+1 hour') OR last_activity IS NULL
ORDER BY last_activity DESC
LIMIT 20;

-- Update thread_cache with correct last_activity values
-- For each thread, find the most recent article that is not hidden
UPDATE thread_cache 
SET last_activity = (
    SELECT MAX(date_sent) 
    FROM articles 
    WHERE article_num IN (
        -- Get all articles in this thread (thread_root + child_articles)
        SELECT thread_root as article_num
        UNION
        SELECT CAST(value AS INTEGER) as article_num
        FROM (
            SELECT trim(value) as value
            FROM (
                SELECT '' as value WHERE child_articles = ''
                UNION ALL
                SELECT substr(child_articles, 1, instr(child_articles || ',', ',') - 1) as value
                FROM thread_cache tc2 
                WHERE tc2.thread_root = thread_cache.thread_root
                  AND child_articles != ''
                UNION ALL
                SELECT trim(substr(child_articles, instr(child_articles, ',') + 1)) as value
                FROM thread_cache tc2 
                WHERE tc2.thread_root = thread_cache.thread_root
                  AND instr(child_articles, ',') > 0
                  AND length(trim(substr(child_articles, instr(child_articles, ',') + 1))) > 0
            )
        )
        WHERE value != '' AND value NOT NULL
    )
    AND hide = 0  -- Only consider non-hidden articles
    AND date_sent <= datetime('now', '+2 hours')  -- Exclude obvious future posts
)
WHERE EXISTS (
    SELECT 1 FROM articles 
    WHERE article_num = thread_cache.thread_root
);

-- Show fixed results
SELECT 
    COUNT(*) as total_threads,
    COUNT(CASE WHEN last_activity > datetime('now', '+1 hour') THEN 1 END) as future_threads,
    COUNT(CASE WHEN last_activity IS NULL THEN 1 END) as null_threads
FROM thread_cache;
