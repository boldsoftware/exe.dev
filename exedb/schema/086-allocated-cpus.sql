ALTER TABLE boxes ADD COLUMN allocated_cpus INTEGER;
ALTER TABLE boxes ADD COLUMN cgroup_overrides TEXT;
ALTER TABLE users ADD COLUMN cgroup_overrides TEXT;
