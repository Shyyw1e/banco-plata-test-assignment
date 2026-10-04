BEGIN;
CREATE TABLE provider_throttle (
    provider text PRIMARY KEY CHECK (provider <> '' AND provider = btrim(provider)),
    next_allowed_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(next_allowed_at)),
    blocked_until timestamptz NOT NULL DEFAULT clock_timestamp() CHECK (isfinite(blocked_until))
);
INSERT INTO provider_throttle (provider) VALUES ('frankfurter');
COMMIT;
