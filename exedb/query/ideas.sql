-- name: ListApprovedTemplates :many
SELECT
    t.*,
    CAST(COALESCE(AVG(r.rating), 0) AS REAL) AS avg_rating,
    CAST(COUNT(r.id) AS INTEGER) AS rating_count
FROM vm_templates t
LEFT JOIN template_ratings r ON r.template_id = t.id
WHERE t.status = 'approved'
GROUP BY t.id
ORDER BY t.featured DESC, t.title ASC;

-- name: GetTemplateBySlug :one
SELECT
    t.*,
    CAST(COALESCE(AVG(r.rating), 0) AS REAL) AS avg_rating,
    CAST(COUNT(r.id) AS INTEGER) AS rating_count
FROM vm_templates t
LEFT JOIN template_ratings r ON r.template_id = t.id
WHERE t.slug = ? AND t.status = 'approved'
GROUP BY t.id;

-- name: GetTemplateByID :one
SELECT * FROM vm_templates WHERE id = ?;

-- name: ListAllTemplates :many
SELECT
    t.*,
    CAST(COALESCE(AVG(r.rating), 0) AS REAL) AS avg_rating,
    CAST(COUNT(r.id) AS INTEGER) AS rating_count
FROM vm_templates t
LEFT JOIN template_ratings r ON r.template_id = t.id
GROUP BY t.id
ORDER BY
    CASE t.status WHEN 'pending' THEN 0 WHEN 'approved' THEN 1 WHEN 'rejected' THEN 2 END,
    t.created_at DESC;

-- name: InsertTemplate :exec
INSERT INTO vm_templates (slug, title, short_description, category, prompt, icon_url, screenshot_url, author_user_id, status, featured, vm_shortname, image)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpdateTemplateStatus :exec
UPDATE vm_templates SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: UpdateTemplate :exec
UPDATE vm_templates
SET title = ?, short_description = ?, category = ?, prompt = ?, icon_url = ?, screenshot_url = ?, featured = ?, vm_shortname = ?, image = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: UpsertTemplateRating :exec
INSERT INTO template_ratings (template_id, user_id, rating)
VALUES (?, ?, ?)
ON CONFLICT(template_id, user_id) DO UPDATE SET rating = excluded.rating, updated_at = CURRENT_TIMESTAMP;

-- name: GetUserTemplateRating :one
SELECT rating FROM template_ratings WHERE template_id = ? AND user_id = ?;

-- name: GetApprovedTemplateByShortname :one
SELECT
    t.*,
    CAST(COALESCE(AVG(r.rating), 0) AS REAL) AS avg_rating,
    CAST(COUNT(r.id) AS INTEGER) AS rating_count
FROM vm_templates t
LEFT JOIN template_ratings r ON r.template_id = t.id
WHERE t.vm_shortname = ? AND t.status = 'approved'
GROUP BY t.id;

-- name: DeleteTemplate :exec
DELETE FROM vm_templates WHERE id = ?;

-- name: ListTemplatesByAuthor :many
SELECT * FROM vm_templates WHERE author_user_id = ? ORDER BY created_at DESC;
