DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM news
        WHERE external_id IS NOT NULL
        GROUP BY external_id
        HAVING COUNT(DISTINCT ticker) > 1
    ) THEN
        RAISE EXCEPTION
            'migration 000009 cannot be rolled back safely: shared news external IDs exist';
    END IF;
END
$$;

DROP INDEX IF EXISTS news_ticker_external_id_idx;

CREATE UNIQUE INDEX news_external_id_idx
    ON news (external_id)
    WHERE external_id IS NOT NULL;
