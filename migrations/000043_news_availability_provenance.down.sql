DROP INDEX IF EXISTS news_availability_idx;
ALTER TABLE news DROP CONSTRAINT IF EXISTS news_availability_known;
ALTER TABLE news DROP COLUMN IF EXISTS availability;
