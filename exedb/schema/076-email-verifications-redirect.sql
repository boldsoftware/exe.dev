-- Store redirect info in email_verifications to avoid spam-filtered URLs
ALTER TABLE email_verifications ADD COLUMN redirect_url TEXT;
ALTER TABLE email_verifications ADD COLUMN return_host TEXT;
