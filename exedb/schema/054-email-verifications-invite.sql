-- Add invite_code_id to email_verifications table for web invite flow
ALTER TABLE email_verifications ADD COLUMN invite_code_id INTEGER REFERENCES invite_codes(id);
