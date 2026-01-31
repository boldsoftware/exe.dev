-- Enable inbound email for VMs at vmname.exe.xyz
ALTER TABLE boxes ADD COLUMN email_receive_enabled INTEGER NOT NULL DEFAULT 0;
