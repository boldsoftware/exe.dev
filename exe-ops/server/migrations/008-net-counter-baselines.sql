ALTER TABLE servers ADD COLUMN net_rx_errors_baseline INTEGER NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN net_rx_dropped_baseline INTEGER NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN net_tx_errors_baseline INTEGER NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN net_tx_dropped_baseline INTEGER NOT NULL DEFAULT 0;
