\set ON_ERROR_STOP on
BEGIN;
-- All fixtures are rolled back; run only against the isolated test database.
INSERT INTO quote_updates (id, pair) VALUES ('00000000-0000-4000-8000-000000000001', 'EUR/USD');
INSERT INTO quote_updates (id, pair, status, attempts, lease_until)
VALUES ('00000000-0000-4000-8000-000000000002', 'USD/EUR', 'processing', 1, now() + interval '30 seconds');
INSERT INTO quote_updates (id, pair, status, attempts, completed_at, price, source_date, source)
VALUES ('00000000-0000-4000-8000-000000000003', 'EUR/MXN', 'succeeded', 1, now(), 20.1234567890, current_date, 'frankfurter:ecb');
INSERT INTO quote_updates (id, pair, status, attempts, completed_at, last_error_code)
VALUES ('00000000-0000-4000-8000-000000000004', 'MXN/EUR', 'failed', 1, now(), 'attempts_exhausted');

DO $$
DECLARE
    statement text;
BEGIN
    FOREACH statement IN ARRAY ARRAY[
        'UPDATE quote_updates SET pair = ''GBP/USD''',
        'UPDATE quote_updates SET id = ''00000000-0000-0000-0000-000000000000'' WHERE status = ''queued''',
        'UPDATE quote_updates SET status = ''unknown''',
        'UPDATE quote_updates SET attempts = -1',
        'UPDATE quote_updates SET lease_until = NULL WHERE status = ''processing''',
        'UPDATE quote_updates SET lease_until = now() WHERE status = ''queued''',
        'UPDATE quote_updates SET completed_at = NULL WHERE status = ''succeeded''',
        'UPDATE quote_updates SET price = NULL WHERE status = ''succeeded''',
        'UPDATE quote_updates SET price = 1 WHERE status = ''queued''',
        'UPDATE quote_updates SET price = ''NaN'' WHERE status = ''succeeded''',
        'UPDATE quote_updates SET price = 0 WHERE status = ''succeeded''',
        'UPDATE quote_updates SET price = -1 WHERE status = ''succeeded''',
        'UPDATE quote_updates SET last_error_code = NULL WHERE status = ''failed''',
        'UPDATE quote_updates SET last_error_code = ''provider_rejected'' WHERE status = ''succeeded''',
        'UPDATE quote_updates SET idempotency_key = ''''',
        'UPDATE quote_updates SET idempotency_key = ''contains space''',
        'UPDATE quote_updates SET idempotency_key = chr(9)',
        'UPDATE quote_updates SET idempotency_key = ''ключ''',
        'UPDATE quote_updates SET idempotency_key = repeat(''a'', 129)',
        'UPDATE quote_updates SET source_date = ''infinity'' WHERE status = ''succeeded'''
    ] LOOP
        BEGIN
            EXECUTE statement;
            RAISE EXCEPTION 'Constraint did not reject: %', statement;
        EXCEPTION WHEN check_violation THEN NULL;
        END;
    END LOOP;
END $$;

UPDATE quote_updates SET idempotency_key = 'Mixed-case_123!' WHERE status = 'queued';
UPDATE quote_updates SET idempotency_key = 'mixed-case_123!' WHERE status = 'processing';
DO $$ BEGIN
    BEGIN
        UPDATE quote_updates SET idempotency_key = 'Mixed-case_123!' WHERE status = 'failed';
        RAISE EXCEPTION 'Duplicate key accepted';
    EXCEPTION WHEN unique_violation THEN NULL;
    END;
    IF NOT EXISTS (SELECT 1 FROM provider_throttle WHERE provider = 'frankfurter') THEN
        RAISE EXCEPTION 'Provider seed missing';
    END IF;
END $$;
ROLLBACK;
