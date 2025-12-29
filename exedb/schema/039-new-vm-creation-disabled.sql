-- Add new_vm_creation_disabled field to users table
-- When set to 1, the user cannot create new VMs

ALTER TABLE users ADD COLUMN new_vm_creation_disabled INTEGER NOT NULL DEFAULT 0;
