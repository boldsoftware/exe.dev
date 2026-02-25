ALTER TABLE checkout_params ADD COLUMN vm_image TEXT NOT NULL DEFAULT '';
ALTER TABLE mobile_pending_vm ADD COLUMN vm_image TEXT DEFAULT '';
