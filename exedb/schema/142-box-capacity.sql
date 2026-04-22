-- Store VM-provisioned disk and memory capacity in exedb so it can be
-- looked up without an RPC to the exelet. 0 is the sentinel for
-- "not filled out yet"; pre-existing rows are backfilled via
-- /debug/exelets/backfill-capacity.
ALTER TABLE boxes ADD COLUMN disk_capacity_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE boxes ADD COLUMN memory_capacity_bytes INTEGER NOT NULL DEFAULT 0;
