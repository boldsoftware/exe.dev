-- name: InsertTeam :exec
INSERT INTO teams (team_id, display_name, limits) VALUES (?, ?, ?);

-- name: GetTeam :one
SELECT * FROM teams WHERE team_id = ?;

-- name: UpdateTeamLimits :exec
UPDATE teams SET limits = ? WHERE team_id = ?;

-- name: SetTeamAuthProvider :exec
UPDATE teams SET auth_provider = ? WHERE team_id = ?;

-- name: GetTeamAuthProvider :one
SELECT auth_provider FROM teams WHERE team_id = ?;

-- name: InsertTeamMember :exec
INSERT INTO team_members (team_id, user_id, role) VALUES (?, ?, ?);

-- name: DeleteTeamMember :exec
DELETE FROM team_members WHERE team_id = ? AND user_id = ?;

-- name: UpdateTeamMemberRole :exec
UPDATE team_members SET role = ? WHERE team_id = ? AND user_id = ?;

-- name: GetTeamForUser :one
SELECT t.team_id, t.display_name, t.limits, t.created_at, tm.role
FROM teams t
JOIN team_members tm ON t.team_id = tm.team_id
WHERE tm.user_id = ?;

-- name: GetTeamMembers :many
SELECT tm.role, tm.created_at as joined_at, u.user_id, u.email, u.auth_provider
FROM team_members tm
JOIN users u ON tm.user_id = u.user_id
WHERE tm.team_id = ?
ORDER BY tm.role DESC, tm.created_at ASC;

-- name: GetTeamMemberByEmail :one
SELECT tm.team_id, tm.user_id, tm.role, tm.created_at, u.email
FROM team_members tm
JOIN users u ON tm.user_id = u.user_id
WHERE tm.team_id = ? AND u.canonical_email = ?;

-- name: IsUserTeamOwner :one
SELECT role = 'owner' as is_owner
FROM team_members
WHERE user_id = ?;

-- name: CountTeamBoxes :one
SELECT COUNT(*) as count
FROM boxes b
JOIN team_members tm ON b.created_by_user_id = tm.user_id
WHERE tm.team_id = (SELECT tm2.team_id FROM team_members tm2 WHERE tm2.user_id = ?);

-- name: ListTeamBoxesForOwner :many
SELECT b.id, b.name, b.status, b.image, b.created_at, b.updated_at, b.region,
       u.email as creator_email
FROM boxes b
JOIN team_members tm_creator ON b.created_by_user_id = tm_creator.user_id
JOIN users u ON b.created_by_user_id = u.user_id
WHERE tm_creator.team_id = (SELECT tm2.team_id FROM team_members tm2 WHERE tm2.user_id = @owner_user_id)
AND b.created_by_user_id != @owner_user_id
AND b.status != 'failed'
ORDER BY b.updated_at DESC;

-- name: GetBoxAccessibleByTeamOwner :one
SELECT b.*
FROM boxes b
JOIN team_members tm_creator ON b.created_by_user_id = tm_creator.user_id
JOIN team_members tm_owner ON tm_creator.team_id = tm_owner.team_id
WHERE b.name = @box_name
AND tm_owner.user_id = @owner_user_id
AND tm_owner.role = 'owner';

-- name: GetBoxByTeamOwnerAndShard :one
SELECT b.*
FROM boxes b
JOIN box_ip_shard bis ON b.id = bis.box_id
JOIN team_members tm_creator ON bis.user_id = tm_creator.user_id
JOIN team_members tm_owner ON tm_creator.team_id = tm_owner.team_id
WHERE bis.ip_shard = @shard
AND tm_owner.user_id = @owner_user_id
AND tm_owner.role = 'owner';

-- name: ListBoxIDsForUser :many
SELECT b.id
FROM boxes b
WHERE b.created_by_user_id = ?;

-- name: ListIPShardsForTeam :many
SELECT bis.ip_shard
FROM box_ip_shard bis
JOIN team_members tm ON bis.user_id = tm.user_id
WHERE tm.team_id = (SELECT tm2.team_id FROM team_members tm2 WHERE tm2.user_id = ?)
ORDER BY bis.ip_shard ASC;

-- name: InsertBoxTeamShare :exec
INSERT INTO box_team_shares (box_id, team_id, shared_by) VALUES (?, ?, ?);

-- name: DeleteBoxTeamShare :exec
DELETE FROM box_team_shares WHERE box_id = ? AND team_id = ?;

-- name: GetBoxTeamShare :one
SELECT * FROM box_team_shares WHERE box_id = ? AND team_id = ?;

-- name: IsBoxSharedWithUserTeam :one
SELECT COUNT(*) > 0 as shared
FROM box_team_shares bts
JOIN team_members tm ON bts.team_id = tm.team_id
WHERE bts.box_id = @box_id AND tm.user_id = @user_id;

-- name: GetBoxTeamSharesByBoxID :many
SELECT bts.*, t.display_name as team_name
FROM box_team_shares bts
JOIN teams t ON bts.team_id = t.team_id
WHERE bts.box_id = ?;

-- name: ListAllTeams :many
SELECT t.team_id, t.display_name, t.created_at,
       (SELECT COUNT(*) FROM team_members tm WHERE tm.team_id = t.team_id) as member_count
FROM teams t
ORDER BY t.created_at DESC;

-- name: DeletePendingTeamInvitesByUser :exec
DELETE FROM pending_team_invites WHERE invited_by_user_id = ?;

-- name: InsertPendingTeamInvite :exec
INSERT INTO pending_team_invites (team_id, email, canonical_email, invited_by_user_id, token, expires_at, auth_provider)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(team_id, canonical_email) DO UPDATE SET
    token = excluded.token,
    expires_at = excluded.expires_at,
    invited_by_user_id = excluded.invited_by_user_id,
    auth_provider = excluded.auth_provider;

-- name: GetPendingTeamInviteByToken :one
SELECT id, team_id, email, canonical_email, invited_by_user_id, token, expires_at, created_at, accepted_at, accepted_by_user_id, auth_provider
FROM pending_team_invites
WHERE token = ? AND accepted_at IS NULL AND expires_at > CURRENT_TIMESTAMP;

-- name: GetPendingTeamInvitesByEmail :many
SELECT pti.id, pti.team_id, pti.email, pti.canonical_email, pti.invited_by_user_id, pti.token, pti.expires_at, pti.created_at, t.display_name as team_name
FROM pending_team_invites pti
JOIN teams t ON pti.team_id = t.team_id
WHERE pti.canonical_email = ? AND pti.accepted_at IS NULL AND pti.expires_at > CURRENT_TIMESTAMP;

-- name: GetPendingTeamInvitesByTeam :many
SELECT id, team_id, email, canonical_email, invited_by_user_id, token, expires_at, created_at, accepted_at, accepted_by_user_id
FROM pending_team_invites
WHERE team_id = ? AND accepted_at IS NULL AND expires_at > CURRENT_TIMESTAMP
ORDER BY created_at DESC;

-- name: MarkPendingTeamInviteAccepted :exec
UPDATE pending_team_invites
SET accepted_at = CURRENT_TIMESTAMP, accepted_by_user_id = ?
WHERE id = ?;

-- name: DeleteExpiredPendingTeamInvites :exec
DELETE FROM pending_team_invites
WHERE expires_at < CURRENT_TIMESTAMP AND accepted_at IS NULL;
