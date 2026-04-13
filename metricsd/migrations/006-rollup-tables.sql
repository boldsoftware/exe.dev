ALTER TABLE vm_metrics ADD COLUMN IF NOT EXISTS vm_id VARCHAR DEFAULT '';

CREATE TABLE IF NOT EXISTS vm_metrics_hourly (
    hour_start              TIMESTAMPTZ NOT NULL,
    day_start               DATE NOT NULL,
    host                    VARCHAR NOT NULL,
    vm_id                   VARCHAR DEFAULT '',
    vm_name                 VARCHAR NOT NULL,
    resource_group          VARCHAR NOT NULL,
    disk_logical_max_bytes  BIGINT NOT NULL,
    disk_compressed_max_bytes BIGINT NOT NULL,
    disk_provisioned_bytes  BIGINT NOT NULL,
    network_tx_delta_bytes  BIGINT NOT NULL,
    network_rx_delta_bytes  BIGINT NOT NULL,
    cpu_delta_seconds       DOUBLE NOT NULL,
    io_read_delta_bytes     BIGINT NOT NULL,
    io_write_delta_bytes    BIGINT NOT NULL,
    memory_rss_max_bytes    BIGINT NOT NULL,
    memory_swap_max_bytes   BIGINT NOT NULL,
    sample_count            INTEGER NOT NULL,
    PRIMARY KEY (hour_start, vm_name)
);

CREATE INDEX IF NOT EXISTS idx_hourly_resource_group
    ON vm_metrics_hourly(resource_group, hour_start);

CREATE INDEX IF NOT EXISTS idx_hourly_day_start
    ON vm_metrics_hourly(day_start, resource_group);

CREATE TABLE IF NOT EXISTS vm_metrics_daily (
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
    PRIMARY KEY (day_start, vm_name)
);

CREATE INDEX IF NOT EXISTS idx_daily_resource_group
    ON vm_metrics_daily(resource_group, day_start);

CREATE SEQUENCE IF NOT EXISTS rollup_log_id_seq;

CREATE TABLE IF NOT EXISTS rollup_log (
    id              INTEGER DEFAULT nextval('rollup_log_id_seq') PRIMARY KEY,
    granularity     VARCHAR NOT NULL,
    period_start    TIMESTAMPTZ NOT NULL,
    period_end      TIMESTAMPTZ NOT NULL,
    rows_written    INTEGER NOT NULL,
    created_at      TIMESTAMPTZ DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_rollup_log
    ON rollup_log(granularity, period_start);
