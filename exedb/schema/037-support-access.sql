-- Add support access flags for exe.dev support SSH access

-- Add support_access_allowed flag to boxes (per-box flag set via REPL command)
ALTER TABLE boxes ADD COLUMN support_access_allowed INTEGER NOT NULL DEFAULT 0;

-- Add root_support flag to users (set via /debug/users admin page)
ALTER TABLE users ADD COLUMN root_support INTEGER NOT NULL DEFAULT 0;
