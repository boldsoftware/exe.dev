DROP INDEX IF EXISTS idx_billing_account_user_id;
DROP INDEX IF EXISTS idx_users_default_billing_account;
DROP INDEX IF EXISTS idx_allocs_billing_account;
ALTER TABLE users DROP COLUMN default_billing_account_id;
ALTER TABLE allocs DROP COLUMN billing_account_id;
DROP TABLE IF EXISTS usage_debits;
DROP TABLE IF EXISTS usage_credits;
DROP TABLE IF EXISTS billing_accounts;
