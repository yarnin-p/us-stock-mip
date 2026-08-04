-- A backfilled headline and one seen live are not interchangeable: the first
-- has an availability timestamp we reconstructed, the second one we observed.
-- Without the distinction a gap-filling backfill silently grants hindsight to
-- every point-in-time feature computed over the repaired window.
ALTER TABLE news
    ADD COLUMN IF NOT EXISTS availability TEXT NOT NULL DEFAULT 'observed';

ALTER TABLE news
    DROP CONSTRAINT IF EXISTS news_availability_known;
ALTER TABLE news
    ADD CONSTRAINT news_availability_known
    CHECK (availability IN ('observed', 'reconstructed'));

COMMENT ON COLUMN news.availability IS
    'observed: available_at is when this row was actually received. '
    'reconstructed: back-filled later and available_at is the publication '
    'time, which is optimistic. Point-in-time evaluation must treat a '
    'reconstructed row as weaker evidence than an observed one.';

-- Rows loaded by the one-off historical import predate live ingestion, so
-- their availability was never observed.
UPDATE news
SET availability = 'reconstructed'
WHERE created_at < '2026-07-29'::date
    AND availability = 'observed';

CREATE INDEX IF NOT EXISTS news_availability_idx
    ON news (availability, available_at DESC);
