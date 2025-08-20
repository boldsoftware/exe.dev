INSERT INTO migrations (migration_number, migration_name) VALUES (003, '003_add_routes_column');

ALTER TABLE machines ADD COLUMN routes TEXT;