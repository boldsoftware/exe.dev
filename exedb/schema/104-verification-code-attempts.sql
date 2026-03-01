ALTER TABLE email_verifications ADD COLUMN verification_code_attempts INTEGER NOT NULL DEFAULT 0;
