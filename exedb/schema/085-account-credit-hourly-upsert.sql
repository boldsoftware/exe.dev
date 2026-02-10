ALTER TABLE account_credit_ledger ADD COLUMN hour_bucket DATETIME;
ALTER TABLE account_credit_ledger ADD COLUMN credit_type TEXT;

UPDATE account_credit_ledger
SET hour_bucket = strftime('%Y-%m-%d %H:00:00', created_at)
WHERE hour_bucket IS NULL;

UPDATE account_credit_ledger
SET credit_type = 'usage'
WHERE credit_type IS NULL
  AND stripe_event_id IS NULL
  AND amount < 0;

CREATE TEMP TABLE account_credit_ledger_usage_rollup AS
SELECT
    MIN(id) AS keep_id,
    account_id,
    hour_bucket,
    credit_type,
    SUM(amount) AS total_amount
FROM account_credit_ledger
WHERE credit_type IS NOT NULL
GROUP BY account_id, hour_bucket, credit_type;

UPDATE account_credit_ledger
SET amount = (
    SELECT r.total_amount
    FROM account_credit_ledger_usage_rollup r
    WHERE r.keep_id = account_credit_ledger.id
)
WHERE id IN (SELECT keep_id FROM account_credit_ledger_usage_rollup);

DELETE FROM account_credit_ledger
WHERE credit_type IS NOT NULL
  AND id NOT IN (SELECT keep_id FROM account_credit_ledger_usage_rollup);

DROP TABLE account_credit_ledger_usage_rollup;

CREATE UNIQUE INDEX idx_account_credit_ledger_account_hour_type
ON account_credit_ledger(account_id, hour_bucket, credit_type);
