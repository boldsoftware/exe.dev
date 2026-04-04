-- name: ListPrivateExelets :many
SELECT exelet_addr FROM private_exelets ORDER BY exelet_addr;

-- name: InsertPrivateExelet :exec
INSERT INTO private_exelets (exelet_addr) VALUES (?) ON CONFLICT DO NOTHING;

-- name: DeletePrivateExelet :exec
DELETE FROM private_exelets WHERE exelet_addr = ?;

-- name: ListTeamExelets :many
SELECT team_id, exelet_addr FROM team_exelets ORDER BY team_id, exelet_addr;

-- name: ListTeamExeletsForTeam :many
SELECT exelet_addr FROM team_exelets WHERE team_id = ? ORDER BY exelet_addr;

-- name: InsertTeamExelet :exec
INSERT INTO team_exelets (team_id, exelet_addr) VALUES (?, ?) ON CONFLICT DO NOTHING;

-- name: DeleteTeamExelet :exec
DELETE FROM team_exelets WHERE team_id = ? AND exelet_addr = ?;

-- name: DeleteTeamExeletsByTeamID :exec
DELETE FROM team_exelets WHERE team_id = ?;

-- name: DeleteTeamExeletsByAddr :exec
DELETE FROM team_exelets WHERE exelet_addr = ?;
