-- Delete box_ip_shard rows whose box_id no longer exists in the boxes table.
-- These were leaked by error cleanup paths that deleted the box row without
-- first deleting the IP shard allocation.
DELETE FROM box_ip_shard
WHERE box_id NOT IN (SELECT id FROM boxes);
