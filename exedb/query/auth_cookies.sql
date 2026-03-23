-- name: InsertAuthCookie :exec
INSERT INTO auth_cookies (cookie_value, user_id, domain, expires_at)
VALUES (?, ?, ?, ?);

-- name: GetAuthCookieInfo :one
SELECT user_id, expires_at
FROM auth_cookies
WHERE cookie_value = ? AND domain = ?;

-- name: DeleteAuthCookie :exec
DELETE FROM auth_cookies WHERE cookie_value = ?;

-- name: UpdateAuthCookieLastUsed :exec
-- Callers should deduplicate per UTC day, but this is also safe to call repeatedly.
UPDATE auth_cookies SET last_used_at = DATE('now') WHERE cookie_value = ? AND last_used_at < DATE('now');

-- name: DeleteAuthCookiesByUserID :exec
DELETE FROM auth_cookies WHERE user_id = ?;

-- name: DeleteAuthCookiesByDomain :exec
DELETE FROM auth_cookies WHERE domain = ?;

-- name: UserHasAuthCookie :one
SELECT EXISTS (
    SELECT 1
    FROM auth_cookies
    WHERE user_id = ?
      AND expires_at > CURRENT_TIMESTAMP
);

-- name: GetSiteCookiesForUser :many
SELECT domain, last_used_at
FROM auth_cookies
WHERE user_id = ? AND domain != ? AND expires_at > CURRENT_TIMESTAMP
ORDER BY last_used_at DESC NULLS LAST;
