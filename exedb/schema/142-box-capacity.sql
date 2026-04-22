-- Store VM-provisioned disk and memory capacity in exedb so it can be
-- looked up without an RPC to the exelet.
ALTER TABLE boxes ADD COLUMN disk_capacity_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE boxes ADD COLUMN memory_capacity_bytes INTEGER NOT NULL DEFAULT 0;
