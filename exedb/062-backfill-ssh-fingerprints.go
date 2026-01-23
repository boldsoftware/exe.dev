package exedb

import (
	"database/sql"

	"exe.dev/sshkey"
	"golang.org/x/crypto/ssh"
)

// backfillSSHFingerprints computes and stores fingerprints for any SSH keys
// that don't have one. This handles existing keys from before fingerprints were added.
func backfillSSHFingerprints(tx *sql.Tx) error {
	// Get all keys without fingerprints (empty string from the default).
	rows, err := tx.Query("SELECT id, public_key FROM ssh_keys WHERE fingerprint = ''")
	if err != nil {
		return err
	}
	defer rows.Close()

	var updates []struct {
		id          int64
		fingerprint string
	}

	for rows.Next() {
		var id int64
		var pubKeyStr string
		if err := rows.Scan(&id, &pubKeyStr); err != nil {
			return err
		}

		// Parse the public key and compute fingerprint.
		pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pubKeyStr))
		if err != nil {
			// Skip keys that can't be parsed - they're likely corrupted.
			continue
		}

		fp := sshkey.FingerprintForKey(pubKey)
		updates = append(updates, struct {
			id          int64
			fingerprint string
		}{id, fp})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	// Apply updates.
	for _, u := range updates {
		if _, err := tx.Exec("UPDATE ssh_keys SET fingerprint = ? WHERE id = ?", u.fingerprint, u.id); err != nil {
			return err
		}
	}

	return nil
}
