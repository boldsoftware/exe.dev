-- Add billing_status column to accounts table to track Stripe checkout completion.
-- Status is 'pending' when checkout is started but not completed, 'active' when complete.
-- This prevents users from bypassing billing by hitting the back button during checkout.
ALTER TABLE accounts ADD COLUMN billing_status TEXT NOT NULL DEFAULT 'pending';

-- Mark all existing accounts as active (they completed the old flow)
UPDATE accounts SET billing_status = 'active';
