ALTER TABLE user_llm_credit
ADD COLUMN billing_upgrade_bonus_granted INTEGER NOT NULL DEFAULT 0;
