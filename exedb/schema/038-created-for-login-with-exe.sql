-- Add created_for_login_with_exe flag to users table
-- This flag is set to true when a user is created during the login/registration flow
-- when trying to log into a site hosted by exe (i.e., via proxy auth with return_host)

ALTER TABLE users ADD COLUMN created_for_login_with_exe INTEGER NOT NULL DEFAULT 0;
