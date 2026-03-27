-- Drop legacy IP shard tables (AWS, Latitude, and the serving/toggle table).
-- All shard resolution now uses only netactuate_ip_shards.
DROP TABLE IF EXISTS ip_shards;
DROP TABLE IF EXISTS aws_ip_shards;
DROP TABLE IF EXISTS latitude_ip_shards;

-- Drop unused columns from user_defaults.
-- global_load_balancer was never read; anycast_network is no longer used.
ALTER TABLE user_defaults DROP COLUMN global_load_balancer;
ALTER TABLE user_defaults DROP COLUMN anycast_network;
