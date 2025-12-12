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
UPDATE auth_cookies SET last_used_at = CURRENT_TIMESTAMP WHERE cookie_value = ?;

-- name: DeleteAuthCookiesByUserID :exec
DELETE FROM auth_cookies WHERE user_id = ?;

-- name: DeleteAuthCookieByValue :exec
DELETE FROM auth_cookies WHERE cookie_value = ?;

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
