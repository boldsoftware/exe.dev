-- Normalize all DATETIME columns to YYYY-MM-DD HH:MM:SS (UTC) format,
-- matching SQLite's CURRENT_TIMESTAMP output.
--
-- parse_timestamp handles all existing formats:
--   - Go time.String():      "2026-01-24 15:28:48.123 +0000 UTC"
--   - Go time.String()+mono: "2026-01-24 15:28:48.123 +0000 UTC m=+123.456"
--   - Time10:                "2026-01-24 15:28:48.123+00:00"
--   - ISO 8601:              "2026-01-24T15:28:48Z" (teams tables)
--   - CURRENT_TIMESTAMP:     "2026-01-24 15:28:48" (already correct, no-op)
--
-- The WHERE clause skips NULL values, already-correct values,
-- and values that parse_timestamp cannot parse (returns NULL),
-- such as DATE('now') date-only strings in last_used_at columns.

UPDATE ssh_host_key SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
UPDATE ssh_host_key SET updated_at = parse_timestamp(updated_at) WHERE updated_at IS NOT NULL AND parse_timestamp(updated_at) IS NOT NULL AND updated_at != parse_timestamp(updated_at);

UPDATE users SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);

UPDATE email_verifications SET expires_at = parse_timestamp(expires_at) WHERE expires_at IS NOT NULL AND parse_timestamp(expires_at) IS NOT NULL AND expires_at != parse_timestamp(expires_at);
UPDATE email_verifications SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);

UPDATE auth_cookies SET expires_at = parse_timestamp(expires_at) WHERE expires_at IS NOT NULL AND parse_timestamp(expires_at) IS NOT NULL AND expires_at != parse_timestamp(expires_at);
UPDATE auth_cookies SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
-- auth_cookies.last_used_at may contain DATE('now') date-only values; parse_timestamp returns NULL for those, so they're skipped.
UPDATE auth_cookies SET last_used_at = parse_timestamp(last_used_at) WHERE last_used_at IS NOT NULL AND parse_timestamp(last_used_at) IS NOT NULL AND last_used_at != parse_timestamp(last_used_at);

UPDATE pending_ssh_keys SET expires_at = parse_timestamp(expires_at) WHERE expires_at IS NOT NULL AND parse_timestamp(expires_at) IS NOT NULL AND expires_at != parse_timestamp(expires_at);
UPDATE pending_ssh_keys SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);

UPDATE migrations SET executed_at = parse_timestamp(executed_at) WHERE executed_at IS NOT NULL AND parse_timestamp(executed_at) IS NOT NULL AND executed_at != parse_timestamp(executed_at);

UPDATE auth_tokens SET expires_at = parse_timestamp(expires_at) WHERE expires_at IS NOT NULL AND parse_timestamp(expires_at) IS NOT NULL AND expires_at != parse_timestamp(expires_at);
UPDATE auth_tokens SET used_at = parse_timestamp(used_at) WHERE used_at IS NOT NULL AND parse_timestamp(used_at) IS NOT NULL AND used_at != parse_timestamp(used_at);
UPDATE auth_tokens SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);

UPDATE server_meta SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
UPDATE server_meta SET updated_at = parse_timestamp(updated_at) WHERE updated_at IS NOT NULL AND parse_timestamp(updated_at) IS NOT NULL AND updated_at != parse_timestamp(updated_at);

UPDATE user_events SET first_occurred_at = parse_timestamp(first_occurred_at) WHERE first_occurred_at IS NOT NULL AND parse_timestamp(first_occurred_at) IS NOT NULL AND first_occurred_at != parse_timestamp(first_occurred_at);
UPDATE user_events SET last_occurred_at = parse_timestamp(last_occurred_at) WHERE last_occurred_at IS NOT NULL AND parse_timestamp(last_occurred_at) IS NOT NULL AND last_occurred_at != parse_timestamp(last_occurred_at);

UPDATE waitlist SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);

UPDATE mobile_pending_vm SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);

UPDATE pending_box_shares SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);

UPDATE box_shares SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);

UPDATE box_share_links SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
UPDATE box_share_links SET last_used_at = parse_timestamp(last_used_at) WHERE last_used_at IS NOT NULL AND parse_timestamp(last_used_at) IS NOT NULL AND last_used_at != parse_timestamp(last_used_at);

UPDATE boxes SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
UPDATE boxes SET updated_at = parse_timestamp(updated_at) WHERE updated_at IS NOT NULL AND parse_timestamp(updated_at) IS NOT NULL AND updated_at != parse_timestamp(updated_at);
UPDATE boxes SET last_started_at = parse_timestamp(last_started_at) WHERE last_started_at IS NOT NULL AND parse_timestamp(last_started_at) IS NOT NULL AND last_started_at != parse_timestamp(last_started_at);

UPDATE deleted_boxes SET deleted_at = parse_timestamp(deleted_at) WHERE deleted_at IS NOT NULL AND parse_timestamp(deleted_at) IS NOT NULL AND deleted_at != parse_timestamp(deleted_at);

UPDATE passkeys SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
UPDATE passkeys SET last_used_at = parse_timestamp(last_used_at) WHERE last_used_at IS NOT NULL AND parse_timestamp(last_used_at) IS NOT NULL AND last_used_at != parse_timestamp(last_used_at);

UPDATE passkey_challenges SET expires_at = parse_timestamp(expires_at) WHERE expires_at IS NOT NULL AND parse_timestamp(expires_at) IS NOT NULL AND expires_at != parse_timestamp(expires_at);
UPDATE passkey_challenges SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);

UPDATE email_address_quality SET queried_at = parse_timestamp(queried_at) WHERE queried_at IS NOT NULL AND parse_timestamp(queried_at) IS NOT NULL AND queried_at != parse_timestamp(queried_at);

UPDATE ip_shards SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
UPDATE ip_shards SET updated_at = parse_timestamp(updated_at) WHERE updated_at IS NOT NULL AND parse_timestamp(updated_at) IS NOT NULL AND updated_at != parse_timestamp(updated_at);

UPDATE accounts SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);

UPDATE hll_sketches SET updated_at = parse_timestamp(updated_at) WHERE updated_at IS NOT NULL AND parse_timestamp(updated_at) IS NOT NULL AND updated_at != parse_timestamp(updated_at);

UPDATE email_bounces SET bounced_at = parse_timestamp(bounced_at) WHERE bounced_at IS NOT NULL AND parse_timestamp(bounced_at) IS NOT NULL AND bounced_at != parse_timestamp(bounced_at);

UPDATE signup_rejections SET rejected_at = parse_timestamp(rejected_at) WHERE rejected_at IS NOT NULL AND parse_timestamp(rejected_at) IS NOT NULL AND rejected_at != parse_timestamp(rejected_at);

UPDATE email_quality_bypass SET added_at = parse_timestamp(added_at) WHERE added_at IS NOT NULL AND parse_timestamp(added_at) IS NOT NULL AND added_at != parse_timestamp(added_at);

UPDATE shell_history SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);

UPDATE invite_code_pool SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);

UPDATE invite_codes SET assigned_at = parse_timestamp(assigned_at) WHERE assigned_at IS NOT NULL AND parse_timestamp(assigned_at) IS NOT NULL AND assigned_at != parse_timestamp(assigned_at);
UPDATE invite_codes SET used_at = parse_timestamp(used_at) WHERE used_at IS NOT NULL AND parse_timestamp(used_at) IS NOT NULL AND used_at != parse_timestamp(used_at);
UPDATE invite_codes SET allocated_at = parse_timestamp(allocated_at) WHERE allocated_at IS NOT NULL AND parse_timestamp(allocated_at) IS NOT NULL AND allocated_at != parse_timestamp(allocated_at);

UPDATE pending_registrations SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
UPDATE pending_registrations SET expires_at = parse_timestamp(expires_at) WHERE expires_at IS NOT NULL AND parse_timestamp(expires_at) IS NOT NULL AND expires_at != parse_timestamp(expires_at);

UPDATE user_llm_credit SET last_refresh_at = parse_timestamp(last_refresh_at) WHERE last_refresh_at IS NOT NULL AND parse_timestamp(last_refresh_at) IS NOT NULL AND last_refresh_at != parse_timestamp(last_refresh_at);
UPDATE user_llm_credit SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
UPDATE user_llm_credit SET updated_at = parse_timestamp(updated_at) WHERE updated_at IS NOT NULL AND parse_timestamp(updated_at) IS NOT NULL AND updated_at != parse_timestamp(updated_at);

-- ssh_keys.last_used_at may contain DATE('now') date-only values; parse_timestamp returns NULL for those, so they're skipped.
UPDATE ssh_keys SET added_at = parse_timestamp(added_at) WHERE added_at IS NOT NULL AND parse_timestamp(added_at) IS NOT NULL AND added_at != parse_timestamp(added_at);
UPDATE ssh_keys SET last_used_at = parse_timestamp(last_used_at) WHERE last_used_at IS NOT NULL AND parse_timestamp(last_used_at) IS NOT NULL AND last_used_at != parse_timestamp(last_used_at);

UPDATE billing_events SET event_at = parse_timestamp(event_at) WHERE event_at IS NOT NULL AND parse_timestamp(event_at) IS NOT NULL AND event_at != parse_timestamp(event_at);
UPDATE billing_events SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);

UPDATE user_defaults SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
UPDATE user_defaults SET updated_at = parse_timestamp(updated_at) WHERE updated_at IS NOT NULL AND parse_timestamp(updated_at) IS NOT NULL AND updated_at != parse_timestamp(updated_at);

UPDATE aws_ip_shards SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
UPDATE aws_ip_shards SET updated_at = parse_timestamp(updated_at) WHERE updated_at IS NOT NULL AND parse_timestamp(updated_at) IS NOT NULL AND updated_at != parse_timestamp(updated_at);

UPDATE latitude_ip_shards SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
UPDATE latitude_ip_shards SET updated_at = parse_timestamp(updated_at) WHERE updated_at IS NOT NULL AND parse_timestamp(updated_at) IS NOT NULL AND updated_at != parse_timestamp(updated_at);

UPDATE box_email_credit SET last_refresh_at = parse_timestamp(last_refresh_at) WHERE last_refresh_at IS NOT NULL AND parse_timestamp(last_refresh_at) IS NOT NULL AND last_refresh_at != parse_timestamp(last_refresh_at);
UPDATE box_email_credit SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);

-- Tables from later migrations (079+):

UPDATE billing_credits SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
UPDATE billing_credits SET hour_bucket = parse_timestamp(hour_bucket) WHERE hour_bucket IS NOT NULL AND parse_timestamp(hour_bucket) IS NOT NULL AND hour_bucket != parse_timestamp(hour_bucket);

UPDATE checkout_params SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);

UPDATE teams SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);

UPDATE team_members SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);

UPDATE box_team_shares SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);

UPDATE pending_team_invites SET expires_at = parse_timestamp(expires_at) WHERE expires_at IS NOT NULL AND parse_timestamp(expires_at) IS NOT NULL AND expires_at != parse_timestamp(expires_at);
UPDATE pending_team_invites SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
UPDATE pending_team_invites SET accepted_at = parse_timestamp(accepted_at) WHERE accepted_at IS NOT NULL AND parse_timestamp(accepted_at) IS NOT NULL AND accepted_at != parse_timestamp(accepted_at);

UPDATE vm_templates SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
UPDATE vm_templates SET updated_at = parse_timestamp(updated_at) WHERE updated_at IS NOT NULL AND parse_timestamp(updated_at) IS NOT NULL AND updated_at != parse_timestamp(updated_at);

UPDATE template_ratings SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
UPDATE template_ratings SET updated_at = parse_timestamp(updated_at) WHERE updated_at IS NOT NULL AND parse_timestamp(updated_at) IS NOT NULL AND updated_at != parse_timestamp(updated_at);

UPDATE oauth_states SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
UPDATE oauth_states SET expires_at = parse_timestamp(expires_at) WHERE expires_at IS NOT NULL AND parse_timestamp(expires_at) IS NOT NULL AND expires_at != parse_timestamp(expires_at);

UPDATE redirects SET expires_at = parse_timestamp(expires_at) WHERE expires_at IS NOT NULL AND parse_timestamp(expires_at) IS NOT NULL AND expires_at != parse_timestamp(expires_at);

UPDATE team_sso_providers SET last_discovery_at = parse_timestamp(last_discovery_at) WHERE last_discovery_at IS NOT NULL AND parse_timestamp(last_discovery_at) IS NOT NULL AND last_discovery_at != parse_timestamp(last_discovery_at);
UPDATE team_sso_providers SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
UPDATE team_sso_providers SET updated_at = parse_timestamp(updated_at) WHERE updated_at IS NOT NULL AND parse_timestamp(updated_at) IS NOT NULL AND updated_at != parse_timestamp(updated_at);

UPDATE app_tokens SET expires_at = parse_timestamp(expires_at) WHERE expires_at IS NOT NULL AND parse_timestamp(expires_at) IS NOT NULL AND expires_at != parse_timestamp(expires_at);
UPDATE app_tokens SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
UPDATE app_tokens SET last_used_at = parse_timestamp(last_used_at) WHERE last_used_at IS NOT NULL AND parse_timestamp(last_used_at) IS NOT NULL AND last_used_at != parse_timestamp(last_used_at);

UPDATE integrations SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);

UPDATE exe1_tokens SET expires_at = parse_timestamp(expires_at) WHERE expires_at IS NOT NULL AND parse_timestamp(expires_at) IS NOT NULL AND expires_at != parse_timestamp(expires_at);
UPDATE exe1_tokens SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);

UPDATE github_user_tokens SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
UPDATE github_user_tokens SET token_renewed_at = parse_timestamp(token_renewed_at) WHERE token_renewed_at IS NOT NULL AND parse_timestamp(token_renewed_at) IS NOT NULL AND token_renewed_at != parse_timestamp(token_renewed_at);
UPDATE github_user_tokens SET access_token_expires_at = parse_timestamp(access_token_expires_at) WHERE access_token_expires_at IS NOT NULL AND parse_timestamp(access_token_expires_at) IS NOT NULL AND access_token_expires_at != parse_timestamp(access_token_expires_at);
UPDATE github_user_tokens SET refresh_token_expires_at = parse_timestamp(refresh_token_expires_at) WHERE refresh_token_expires_at IS NOT NULL AND parse_timestamp(refresh_token_expires_at) IS NOT NULL AND refresh_token_expires_at != parse_timestamp(refresh_token_expires_at);

UPDATE github_installations SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);

UPDATE signup_ip_checks SET checked_at = parse_timestamp(checked_at) WHERE checked_at IS NOT NULL AND parse_timestamp(checked_at) IS NOT NULL AND checked_at != parse_timestamp(checked_at);

UPDATE released_box_names SET released_at = parse_timestamp(released_at) WHERE released_at IS NOT NULL AND parse_timestamp(released_at) IS NOT NULL AND released_at != parse_timestamp(released_at);

UPDATE netactuate_ip_shards SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
UPDATE netactuate_ip_shards SET updated_at = parse_timestamp(updated_at) WHERE updated_at IS NOT NULL AND parse_timestamp(updated_at) IS NOT NULL AND updated_at != parse_timestamp(updated_at);

UPDATE push_tokens SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
UPDATE push_tokens SET last_used_at = parse_timestamp(last_used_at) WHERE last_used_at IS NOT NULL AND parse_timestamp(last_used_at) IS NOT NULL AND last_used_at != parse_timestamp(last_used_at);

UPDATE account_plans SET started_at = parse_timestamp(started_at) WHERE started_at IS NOT NULL AND parse_timestamp(started_at) IS NOT NULL AND started_at != parse_timestamp(started_at);
UPDATE account_plans SET ended_at = parse_timestamp(ended_at) WHERE ended_at IS NOT NULL AND parse_timestamp(ended_at) IS NOT NULL AND ended_at != parse_timestamp(ended_at);
UPDATE account_plans SET trial_expires_at = parse_timestamp(trial_expires_at) WHERE trial_expires_at IS NOT NULL AND parse_timestamp(trial_expires_at) IS NOT NULL AND trial_expires_at != parse_timestamp(trial_expires_at);
UPDATE account_plans SET created_at = parse_timestamp(created_at) WHERE created_at IS NOT NULL AND parse_timestamp(created_at) IS NOT NULL AND created_at != parse_timestamp(created_at);
