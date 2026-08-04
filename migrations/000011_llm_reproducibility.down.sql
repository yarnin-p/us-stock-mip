ALTER TABLE llm_analyses
    DROP CONSTRAINT IF EXISTS llm_analyses_reproducibility_nonempty,
    DROP COLUMN IF EXISTS source_text,
    DROP COLUMN IF EXISTS prompt_version,
    DROP COLUMN IF EXISTS provider_endpoint;
