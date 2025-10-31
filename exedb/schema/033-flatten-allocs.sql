-- Flatten the allocs table into users/boxes

PRAGMA foreign_keys = OFF;

-- Move ctrhost from allocs to boxes
ALTER TABLE boxes ADD COLUMN ctrhost TEXT;
UPDATE boxes SET ctrhost = (
    SELECT a.ctrhost FROM allocs a WHERE a.alloc_id = boxes.alloc_id
);


-- Boxes (replace alloc_id with created_by_user_id)
ALTER TABLE boxes RENAME TO original_boxes;
CREATE TABLE boxes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    status TEXT NOT NULL,
    image TEXT NOT NULL,
    ctrhost TEXT NOT NULL,
    container_id TEXT,
    created_by_user_id TEXT NOT NULL, -- <== Existing column now replacing alloc_id
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_started_at DATETIME,
    routes TEXT,
    ssh_server_identity_key BLOB,
    ssh_authorized_keys TEXT,
    ssh_client_private_key BLOB,
    ssh_port INTEGER,
    ssh_user TEXT,
    creation_log TEXT,
    UNIQUE(name),
    FOREIGN KEY (created_by_user_id) REFERENCES users(user_id) ON DELETE CASCADE
);
INSERT INTO boxes
SELECT 
    id,
    name,
    status,
    image,
    ctrhost,
    container_id,
    created_by_user_id,
    created_at,
    updated_at,
    last_started_at,
    routes,
    ssh_server_identity_key,
    ssh_authorized_keys,
    ssh_client_private_key,
    ssh_port,
    ssh_user,
    creation_log
FROM original_boxes;

-- Deleted boxes (replacing alloc_id with user_id)
ALTER TABLE deleted_boxes RENAME TO original_deleted_boxes;
CREATE TABLE deleted_boxes (
    id INTEGER PRIMARY KEY,
    user_id TEXT NOT NULL,
    deleted_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
);

-- The user_id is taken from the created_by_user_id in the original boxes table,
-- which is the single box in the original allocs table.
INSERT INTO deleted_boxes (id, user_id, deleted_at)
SELECT 
    o.id,
    b.created_by_user_id,
    o.deleted_at
FROM
    original_deleted_boxes o,
    (
        SELECT created_by_user_id
        FROM original_boxes INNER JOIN allocs a ON a.alloc_id = original_boxes.alloc_id
        WHERE a.alloc_id = original_boxes.alloc_id LIMIT 1
    ) b;

DROP TABLE allocs;
DROP TABLE original_boxes;
DROP TABLE original_deleted_boxes;

PRAGMA foreign_keys = ON;
