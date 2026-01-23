package exedb

import (
	"database/sql"
	"fmt"

	"exe.dev/sshkey"
)

// backfillSSHKeyComments assigns generated comments to SSH keys that have NULL comments,
// using the pattern "key-N" where N is assigned per-user based on added_at order.
// It also sanitizes all existing comments.
func backfillSSHKeyComments(tx *sql.Tx) error {
	// Step 1: Get all users who have SSH keys
	rows, err := tx.Query("SELECT DISTINCT user_id FROM ssh_keys")
	if err != nil {
		return fmt.Errorf("failed to get users with SSH keys: %w", err)
	}
	defer rows.Close()

	var userIDs []string
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return fmt.Errorf("failed to scan user_id: %w", err)
		}
		userIDs = append(userIDs, userID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating users: %w", err)
	}
	rows.Close()

	// Step 2: For each user, number their keys and assign comments
	for _, userID := range userIDs {
		// Get user's keys ordered by added_at (oldest first for numbering)
		keyRows, err := tx.Query(`
			SELECT id, comment
			FROM ssh_keys
			WHERE user_id = ?
			ORDER BY added_at ASC, id ASC
		`, userID)
		if err != nil {
			return fmt.Errorf("failed to get keys for user %s: %w", userID, err)
		}

		type keyUpdate struct {
			id      int64
			comment string
		}
		var updates []keyUpdate
		keyNumber := 1

		for keyRows.Next() {
			var id int64
			var comment sql.NullString
			if err := keyRows.Scan(&id, &comment); err != nil {
				keyRows.Close()
				return fmt.Errorf("failed to scan key: %w", err)
			}

			var newComment string
			if !comment.Valid || comment.String == "" {
				// NULL or empty comment - generate key-N
				newComment = sshkey.GeneratedComment(keyNumber)
			} else {
				// Existing comment - sanitize it
				newComment = sshkey.SanitizeComment(comment.String)
				if newComment == "" {
					// Sanitization resulted in empty string - generate key-N
					newComment = sshkey.GeneratedComment(keyNumber)
				}
			}
			updates = append(updates, keyUpdate{id: id, comment: newComment})
			keyNumber++
		}
		keyRows.Close()
		if err := keyRows.Err(); err != nil {
			return fmt.Errorf("error iterating keys: %w", err)
		}

		// Apply updates
		for _, u := range updates {
			if _, err := tx.Exec("UPDATE ssh_keys SET comment = ? WHERE id = ?", u.comment, u.id); err != nil {
				return fmt.Errorf("failed to update key %d: %w", u.id, err)
			}
		}

		// Update user's next_ssh_key_number to be one more than the total keys
		if _, err := tx.Exec("UPDATE users SET next_ssh_key_number = ? WHERE user_id = ?", keyNumber, userID); err != nil {
			return fmt.Errorf("failed to update next_ssh_key_number for user %s: %w", userID, err)
		}
	}

	return nil
}
