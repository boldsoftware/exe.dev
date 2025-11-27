-- Drop unused image metadata columns from tag_resolutions table
ALTER TABLE tag_resolutions DROP COLUMN image_user;
ALTER TABLE tag_resolutions DROP COLUMN image_login_user;
ALTER TABLE tag_resolutions DROP COLUMN image_entrypoint;
ALTER TABLE tag_resolutions DROP COLUMN image_cmd;
ALTER TABLE tag_resolutions DROP COLUMN image_labels;
ALTER TABLE tag_resolutions DROP COLUMN image_exposed_ports;
