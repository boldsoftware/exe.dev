ALTER TABLE email_verifications
ADD COLUMN verification_code TEXT;

INSERT
OR IGNORE INTO migrations (migration_number, migration_name)
VALUES
    (027, '027_email_verification_code');
