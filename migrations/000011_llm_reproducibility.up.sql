ALTER TABLE llm_analyses
    ADD COLUMN provider_endpoint TEXT NOT NULL DEFAULT 'unknown',
    ADD COLUMN prompt_version TEXT NOT NULL DEFAULT 'legacy',
    ADD COLUMN source_text TEXT NOT NULL DEFAULT '';

ALTER TABLE llm_analyses
    ALTER COLUMN provider_endpoint DROP DEFAULT,
    ALTER COLUMN prompt_version DROP DEFAULT,
    ALTER COLUMN source_text DROP DEFAULT;

ALTER TABLE llm_analyses
    ADD CONSTRAINT llm_analyses_reproducibility_nonempty CHECK (
        (
            prompt_version = 'legacy'
            AND provider_endpoint = 'unknown'
            AND source_text = ''
        )
        OR (
            prompt_version <> 'legacy'
            AND length(provider_endpoint) > 0
            AND length(prompt_version) > 0
            AND length(source_text) > 0
        )
    );
