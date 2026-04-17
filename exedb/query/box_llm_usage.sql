-- name: RecordBoxLLMUsage :exec
-- RecordBoxLLMUsage upserts LLM usage for a box, accumulating cost and request
-- count into an hourly bucket per model.
-- user_id is intentionally not part of the conflict key, so usage stays grouped
-- by box/model/provider/hour even if ownership changes mid-hour.
INSERT INTO box_llm_usage (box_id, user_id, provider, model, hour_bucket, cost_microcents, request_count)
VALUES (?1, ?2, ?3, ?4, strftime('%Y-%m-%d %H:00:00', CURRENT_TIMESTAMP), ?5, 1)
ON CONFLICT(box_id, provider, model, hour_bucket)
DO UPDATE SET cost_microcents = box_llm_usage.cost_microcents + excluded.cost_microcents,
              request_count = box_llm_usage.request_count + 1;

-- name: GetBoxLLMUsage :many
-- GetBoxLLMUsage returns LLM usage rows for a box within a time range,
-- ordered by most recent first.
SELECT * FROM box_llm_usage
WHERE box_id = ? AND hour_bucket >= ? AND hour_bucket < ?
ORDER BY hour_bucket DESC, model ASC;

-- name: GetBoxLLMUsageSummary :many
-- GetBoxLLMUsageSummary returns total cost and request count per model
-- for a box within a time range.
SELECT model, provider,
       CAST(SUM(cost_microcents) AS INTEGER) AS total_cost_microcents,
       CAST(SUM(request_count) AS INTEGER) AS total_request_count
FROM box_llm_usage
WHERE box_id = ? AND hour_bucket >= ? AND hour_bucket < ?
GROUP BY model, provider
ORDER BY total_cost_microcents DESC;

-- name: GetUserLLMUsageSummary :many
-- GetUserLLMUsageSummary returns total cost and request count per model
-- across all boxes owned by a user within a time range.
SELECT model, provider,
       CAST(SUM(cost_microcents) AS INTEGER) AS total_cost_microcents,
       CAST(SUM(request_count) AS INTEGER) AS total_request_count
FROM box_llm_usage
WHERE user_id = ? AND hour_bucket >= ? AND hour_bucket < ?
GROUP BY model, provider
ORDER BY total_cost_microcents DESC;

-- name: GetUserLLMUsageDaily :many
-- GetUserLLMUsageDaily returns per-day, per-box, per-model LLM usage
-- for all boxes owned by a user within a time range.
-- day is stored as e.g. "2026-04-17".
SELECT COALESCE(b.name, '(deleted)') AS box_name,
       u.model, u.provider,
       CAST(DATE(u.hour_bucket) AS TEXT) AS day,
       CAST(SUM(u.cost_microcents) AS INTEGER) AS cost_microcents,
       CAST(SUM(u.request_count) AS INTEGER) AS request_count
FROM box_llm_usage u
LEFT JOIN boxes b ON b.id = u.box_id
WHERE u.user_id = ? AND u.hour_bucket >= ? AND u.hour_bucket < ?
GROUP BY COALESCE(b.name, '(deleted)'), u.model, u.provider, DATE(u.hour_bucket)
ORDER BY day DESC, cost_microcents DESC;
