-- rollup_log is no longer needed: rollups are full CTAS rebuilds, inherently idempotent.
DROP TABLE IF EXISTS rollup_log;
DROP SEQUENCE IF EXISTS rollup_log_id_seq;

CREATE TABLE IF NOT EXISTS vm_metrics_monthly (
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
    PRIMARY KEY (month_start, vm_name)
);

CREATE INDEX IF NOT EXISTS idx_monthly_resource_group
    ON vm_metrics_monthly(resource_group, month_start);
