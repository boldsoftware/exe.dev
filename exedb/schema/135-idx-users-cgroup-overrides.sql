CREATE INDEX idx_users_cgroup_overrides_set ON users(user_id) WHERE cgroup_overrides IS NOT NULL;
