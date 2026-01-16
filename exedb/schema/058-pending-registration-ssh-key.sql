-- Add public_key column to pending_registrations for SSH-initiated billing flow
-- When an anonymous SSH user tries to create a VM, we store their public key
-- so it can be associated with their account after billing checkout.
ALTER TABLE pending_registrations ADD COLUMN public_key TEXT;
