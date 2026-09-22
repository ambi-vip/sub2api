-- Optional request-stage timing snapshot for usage detail diagnostics.
-- No default/backfill keeps this metadata-only migration cheap on large or
-- partitioned usage_logs tables; historical rows remain NULL.
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS latency_breakdown JSONB;

COMMENT ON COLUMN usage_logs.latency_breakdown IS
    'Optional request-stage latency snapshot; list APIs omit this payload and detail APIs load it on demand';
