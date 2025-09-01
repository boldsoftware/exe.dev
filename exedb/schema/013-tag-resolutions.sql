-- Migration 013: Add tag resolutions table for tracking image tag to digest mappings
-- This supports the control plane tag resolver to keep images fresh across hosts

-- Create tag_resolutions table
CREATE TABLE IF NOT EXISTS tag_resolutions (
    -- Primary key components
    registry TEXT NOT NULL,      -- e.g., 'docker.io', 'ghcr.io', 'quay.io'
    repository TEXT NOT NULL,     -- e.g., 'library/ubuntu', 'boldsoftware/exeuntu'
    tag TEXT NOT NULL,           -- e.g., 'latest', '22.04', 'v1.0.0'
    
    -- Digest information
    index_digest TEXT,           -- Manifest list/OCI index digest (sha256:...)
    platform_digest TEXT,        -- Platform-specific manifest digest (sha256:...)
    platform TEXT NOT NULL DEFAULT 'linux/amd64', -- Platform identifier
    
    -- Timing information
    last_checked_at INTEGER NOT NULL,  -- Unix timestamp of last upstream check
    last_changed_at INTEGER NOT NULL,  -- Unix timestamp of last digest change
    ttl_seconds INTEGER NOT NULL DEFAULT 21600, -- 6 hours default TTL
    
    -- Tracking information
    seen_on_hosts INTEGER DEFAULT 0,   -- Counter of hosts that have this image
    image_size INTEGER,                 -- Image size in bytes (for progress reporting)
    
    -- Metadata
    created_at INTEGER NOT NULL,       -- Unix timestamp when record was created
    updated_at INTEGER NOT NULL,       -- Unix timestamp when record was last updated
    
    PRIMARY KEY (registry, repository, tag, platform)
);

-- Create indexes for efficient lookups
CREATE INDEX IF NOT EXISTS idx_tag_resolutions_check_time 
    ON tag_resolutions(last_checked_at, ttl_seconds);

CREATE INDEX IF NOT EXISTS idx_tag_resolutions_digest 
    ON tag_resolutions(platform_digest) WHERE platform_digest IS NOT NULL;

-- Create tag_resolution_history table for tracking digest changes over time
CREATE TABLE IF NOT EXISTS tag_resolution_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    registry TEXT NOT NULL,
    repository TEXT NOT NULL,
    tag TEXT NOT NULL,
    platform TEXT NOT NULL,
    old_digest TEXT,
    new_digest TEXT NOT NULL,
    changed_at INTEGER NOT NULL,  -- Unix timestamp
    
    FOREIGN KEY (registry, repository, tag, platform) 
        REFERENCES tag_resolutions(registry, repository, tag, platform)
);

-- Create index for history lookups
CREATE INDEX IF NOT EXISTS idx_tag_resolution_history_time 
    ON tag_resolution_history(changed_at DESC);

-- Insert migration entry
INSERT OR IGNORE INTO migrations (migration_number, migration_name) 
    VALUES (013, '013_tag_resolutions');