ALTER TABLE account_credit_ledger RENAME TO billing_credits;

DROP INDEX IF EXISTS idx_account_credit_ledger_account;
DROP INDEX IF EXISTS idx_account_credit_ledger_account_hour_type;

CREATE INDEX IF NOT EXISTS idx_billing_credits_account
ON billing_credits(account_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_credits_account_hour_type
ON billing_credits(account_id, hour_bucket, credit_type);
