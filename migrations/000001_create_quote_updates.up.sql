BEGIN;

CREATE TABLE quote_updates (
    id uuid PRIMARY KEY CHECK (id <> '00000000-0000-0000-0000-000000000000'::uuid),
    pair text NOT NULL CHECK (pair IN ('EUR/USD', 'USD/EUR', 'EUR/MXN', 'MXN/EUR', 'USD/MXN', 'MXN/USD')),
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'processing', 'succeeded', 'failed')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    lease_until timestamptz,
    completed_at timestamptz,
    price numeric(20,10),
    source_date date,
    source text,
    last_error_code text CHECK (last_error_code IN (
        'provider_unavailable', 'provider_rejected',
        'provider_invalid_response', 'attempts_exhausted'
    )),
    idempotency_key text COLLATE "C",

    CONSTRAINT quote_updates_idempotency_key_unique UNIQUE (idempotency_key),
    CONSTRAINT quote_updates_idempotency_key_valid CHECK (
        idempotency_key IS NULL OR (
            octet_length(idempotency_key) BETWEEN 1 AND 128
            AND idempotency_key ~ '^[!-~]+$'
        )
    ),
    CONSTRAINT quote_updates_lease_state CHECK (
        (status = 'processing') = (lease_until IS NOT NULL)
    ),
    CONSTRAINT quote_updates_completed_state CHECK (
        (status IN ('succeeded', 'failed')) = (completed_at IS NOT NULL)
    ),
    CONSTRAINT quote_updates_attempt_state CHECK (status = 'queued' OR attempts > 0),
    CONSTRAINT quote_updates_result_state CHECK (
        (status = 'succeeded' AND price IS NOT NULL AND source_date IS NOT NULL AND source IS NOT NULL)
        OR
        (status <> 'succeeded' AND price IS NULL AND source_date IS NULL AND source IS NULL)
    ),
    CONSTRAINT quote_updates_price_valid CHECK (
        price IS NULL OR (price > 0 AND price < 10000000000 AND price <> 'NaN'::numeric)
    ),
    CONSTRAINT quote_updates_source_valid CHECK (
        source IS NULL OR (source <> '' AND source = btrim(source))
    ),
    CONSTRAINT quote_updates_error_state CHECK (
        (status <> 'failed' OR last_error_code IS NOT NULL)
        AND (status <> 'succeeded' OR last_error_code IS NULL)
    ),
    CONSTRAINT quote_updates_time_valid CHECK (
        isfinite(created_at) AND isfinite(next_attempt_at)
        AND (lease_until IS NULL OR isfinite(lease_until))
        AND (completed_at IS NULL OR isfinite(completed_at))
        AND (source_date IS NULL OR (source_date BETWEEN DATE '0001-01-01' AND DATE '9999-12-31'))
    )
);

CREATE INDEX quote_updates_ready_idx
    ON quote_updates (next_attempt_at, created_at, id) WHERE status = 'queued';
CREATE INDEX quote_updates_expired_idx
    ON quote_updates (lease_until, id) WHERE status = 'processing';
CREATE INDEX quote_updates_latest_idx
    ON quote_updates (pair, source_date DESC, completed_at DESC, id DESC) WHERE status = 'succeeded';

COMMIT;
