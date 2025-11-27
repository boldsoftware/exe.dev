-- name: GetTagsNeedingRefresh :many
SELECT registry, repository, tag, platform,
       COALESCE(index_digest, '') as index_digest, COALESCE(platform_digest, '') as platform_digest,
       last_checked_at, ttl_seconds
FROM tag_resolutions
WHERE last_checked_at + ttl_seconds < ?
ORDER BY last_checked_at ASC
LIMIT ?;

-- name: UpdateTagResolutionDigest :exec
UPDATE tag_resolutions
SET platform_digest = ?, last_checked_at = ?, last_changed_at = ?,
    updated_at = ?, image_size = ?
WHERE registry = ? AND repository = ? AND tag = ? AND platform = ?;

-- name: InsertTagResolutionHistory :exec
INSERT INTO tag_resolution_history (registry, repository, tag, platform,
                                   old_digest, new_digest, changed_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: UpdateTagResolutionChecked :exec
UPDATE tag_resolutions
SET last_checked_at = ?, updated_at = ?
WHERE registry = ? AND repository = ? AND tag = ? AND platform = ?;

-- name: UpsertTagResolution :exec
INSERT INTO tag_resolutions (registry, repository, tag, platform,
                            platform_digest, last_checked_at, last_changed_at,
                            ttl_seconds, image_size, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(registry, repository, tag, platform) DO UPDATE SET
    platform_digest = excluded.platform_digest,
    last_checked_at = excluded.last_checked_at,
    last_changed_at = CASE
        WHEN platform_digest != excluded.platform_digest THEN excluded.last_changed_at
        ELSE last_changed_at
    END,
    ttl_seconds = excluded.ttl_seconds,
    image_size = excluded.image_size,
    updated_at = excluded.updated_at;


-- name: GetTagResolution :one
SELECT registry, repository, tag, platform,
       COALESCE(index_digest, '') as index_digest, COALESCE(platform_digest, '') as platform_digest,
       last_checked_at, last_changed_at, ttl_seconds, COALESCE(seen_on_hosts, 0) as seen_on_hosts, COALESCE(image_size, 0) as image_size
FROM tag_resolutions
WHERE registry = ? AND repository = ? AND tag = ? AND platform = ?;

-- name: IncrementSeenOnHosts :exec
UPDATE tag_resolutions
SET seen_on_hosts = seen_on_hosts + 1
WHERE registry = ? AND repository = ? AND tag = ? AND platform = ?;
