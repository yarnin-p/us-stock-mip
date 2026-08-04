DROP TABLE IF EXISTS catalyst_watchlist;

ALTER TABLE ah_boundary_outcomes
    DROP COLUMN IF EXISTS news_age_hours,
    DROP COLUMN IF EXISTS news_items_7d,
    DROP COLUMN IF EXISTS post_close_news,
    DROP COLUMN IF EXISTS post_close_news_at,
    DROP COLUMN IF EXISTS post_close_news_title,
    DROP COLUMN IF EXISTS news_feed_rows;
