-- Fix PRIMARY KEYs on rollup tables to use vm_key (COALESCE(vm_id, vm_name))
-- instead of vm_name alone. The rollup SQL groups by vm_key, so two VMs with
-- the same name but different vm_ids cause a PK collision.
--
-- DuckDB doesn't support ALTER TABLE ... DROP CONSTRAINT, so we recreate the tables.

-- Daily: preserve data, recreate with correct PK.
CREATE TABLE vm_metrics_daily_new AS SELECT * FROM vm_metrics_daily;
DROP TABLE vm_metrics_daily;
CREATE TABLE vm_metrics_daily (
    day_start               DATE NOT NULL,
    host                    VARCHAR NOT NULL,
    vm_id                   VARCHAR DEFAULT '',
    vm_name                 VARCHAR NOT NULL,
    resource_group          VARCHAR NOT NULL,
    disk_logical_avg_bytes  BIGINT NOT NULL,
    disk_logical_max_bytes  BIGINT NOT NULL,
    disk_compressed_avg_bytes BIGINT NOT NULL,
    disk_provisioned_max_bytes BIGINT NOT NULL,
    network_tx_bytes        BIGINT NOT NULL,
    network_rx_bytes        BIGINT NOT NULL,
    cpu_seconds             DOUBLE NOT NULL,
    io_read_bytes           BIGINT NOT NULL,
    io_write_bytes          BIGINT NOT NULL,
    memory_rss_max_bytes    BIGINT NOT NULL,
    memory_swap_max_bytes   BIGINT NOT NULL,
    hours_with_data         INTEGER NOT NULL,
    PRIMARY KEY (day_start, vm_id, vm_name)
);
INSERT INTO vm_metrics_daily SELECT * FROM vm_metrics_daily_new;
DROP TABLE vm_metrics_daily_new;
CREATE INDEX IF NOT EXISTS idx_daily_resource_group
    ON vm_metrics_daily(resource_group, day_start);

-- Monthly: recreated by rollup every cycle (CTAS), so just drop and let rollup rebuild.
DROP TABLE IF EXISTS vm_metrics_monthly;
CREATE TABLE vm_metrics_monthly (
    month_start             DATE NOT NULL,
    host                    VARCHAR NOT NULL,
    vm_id                   VARCHAR DEFAULT '',
    vm_name                 VARCHAR NOT NULL,
    resource_group          VARCHAR NOT NULL,
    disk_logical_avg_bytes  BIGINT NOT NULL,
    disk_logical_max_bytes  BIGINT NOT NULL,
    disk_compressed_avg_bytes BIGINT NOT NULL,
    disk_provisioned_max_bytes BIGINT NOT NULL,
    network_tx_bytes        BIGINT NOT NULL,
    network_rx_bytes        BIGINT NOT NULL,
    cpu_seconds             DOUBLE NOT NULL,
    io_read_bytes           BIGINT NOT NULL,
    io_write_bytes          BIGINT NOT NULL,
    memory_rss_max_bytes    BIGINT NOT NULL,
    memory_swap_max_bytes   BIGINT NOT NULL,
    days_with_data          INTEGER NOT NULL,
    PRIMARY KEY (month_start, vm_id, vm_name)
);
CREATE INDEX IF NOT EXISTS idx_monthly_resource_group
    ON vm_metrics_monthly(resource_group, month_start);
