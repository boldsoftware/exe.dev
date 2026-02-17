UPDATE user_llm_credit
SET
    available_credit = 100.0,
    updated_at = CURRENT_TIMESTAMP
WHERE max_credit IS NULL
  AND refresh_per_hour IS NULL
  AND available_credit < 100.0;
