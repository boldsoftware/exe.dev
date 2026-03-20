-- account_plans tracks the plan history for each account.
-- Every plan change inserts a new row. The active plan is the row where ended_at IS NULL.
-- The partial unique index ensures at most one active plan per account at any time.
CREATE TABLE account_plans (
    account_id   TEXT     NOT NULL REFERENCES accounts(id),
    plan_id      TEXT     NOT NULL,
    started_at   DATETIME NOT NULL,
    ended_at     DATETIME,
    trial_expires_at DATETIME,
    changed_by   TEXT,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_account_plans_account ON account_plans(account_id);

-- Partial unique index: only one active (ended_at IS NULL) plan per account.
CREATE UNIQUE INDEX idx_account_plans_active
ON account_plans(account_id) WHERE ended_at IS NULL;
