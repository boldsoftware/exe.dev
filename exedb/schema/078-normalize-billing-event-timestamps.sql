-- Convert billing_events.event_at from Go time.String() format to RFC3339 UTC
--
-- Input formats:
--   "2026-01-31 16:32:38 -0800 PST" (PST is UTC-8, so 16:32 PST = 00:32 UTC next day)
--   "2026-01-25 17:01:29 +0000 UTC" (already UTC)
--   "2026-01-25 17:00:55.295313663 +0000 UTC" (with fractional seconds)
--   "2026-02-03 12:12:42.55229 -0800 PST m=+38.068739085" (with monotonic clock)
--
-- Output format:
--   "2026-02-01T00:32:38Z" (RFC3339 UTC)

UPDATE billing_events
SET event_at = (
    WITH parsed AS (
        SELECT
            -- Strip " m=..." if present
            CASE WHEN instr(event_at, ' m=') > 0
                 THEN substr(event_at, 1, instr(event_at, ' m=') - 1)
                 ELSE event_at
            END AS clean,
            -- Find position of timezone (space followed by +/-)
            CASE
                WHEN instr(substr(event_at, 20), ' +') > 0
                THEN 19 + instr(substr(event_at, 20), ' +')
                WHEN instr(substr(event_at, 20), ' -') > 0
                THEN 19 + instr(substr(event_at, 20), ' -')
                ELSE 0
            END AS tz_pos
    )
    SELECT strftime('%Y-%m-%dT%H:%M:%SZ',
        datetime(
            substr(clean, 1, tz_pos - 1),  -- DateTime part (before timezone)
            -- Timezone modifier: invert sign because SQLite applies offset
            CASE
                WHEN tz_pos > 0 AND substr(clean, tz_pos + 1, 1) = '-'
                THEN '+' || substr(clean, tz_pos + 2, 2) || ':' || substr(clean, tz_pos + 4, 2)
                WHEN tz_pos > 0 AND substr(clean, tz_pos + 1, 1) = '+'
                THEN '-' || substr(clean, tz_pos + 2, 2) || ':' || substr(clean, tz_pos + 4, 2)
                ELSE 'utc'
            END
        )
    )
    FROM parsed
)
WHERE substr(event_at, 11, 1) != 'T';
