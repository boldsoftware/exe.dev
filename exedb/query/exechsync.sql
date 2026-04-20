-- Queries used by package exechsync to copy a small subset of the database
-- to a ClickHouse data warehouse once per day. See exechsync/README.md.

-- name: ExtractUsersForClickHouse :many
SELECT user_id, email
FROM users;

-- name: ExtractTeamsForClickHouse :many
SELECT team_id, display_name
FROM teams;

-- name: ExtractTeamMembersForClickHouse :many
SELECT team_id, user_id, role
FROM team_members;

-- name: ExtractAccountsForClickHouse :many
SELECT id, created_by, parent_id
FROM accounts;

-- name: ExtractAccountPlansForClickHouse :many
SELECT account_id, plan_id, started_at, ended_at, trial_expires_at
FROM account_plans;

-- name: ExtractBoxesForClickHouse :many
SELECT name, created_by_user_id, status, region
FROM boxes;
