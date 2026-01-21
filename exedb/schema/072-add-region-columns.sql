-- Add region column to users table
ALTER TABLE users ADD COLUMN region TEXT NOT NULL DEFAULT 'pdx';

-- Add region column to boxes table
ALTER TABLE boxes ADD COLUMN region TEXT NOT NULL DEFAULT 'pdx';

-- Create index on boxes.region for efficient filtering
CREATE INDEX idx_boxes_region ON boxes(region);
