package exeprox

import (
	"context"

	"github.com/go4org/hashtriemap"
)

// sshKeysData holds the SSH keys, mapped by fingerprint.
// This is a cache of the ssh_keys table in the database.
type sshKeysData struct {
	sshKeys hashtriemap.HashTrieMap[string, sshKeyData]
}

// sshKeyData holds information about a single SSH key.
type sshKeyData struct {
	fingerprint string
	userID      string
	publicKey   string
}

// lookup returns information about an SSH key given the fingerprint.
// The bool result reports whether the key exists.
func (skd *sshKeysData) lookup(ctx context.Context, exeproxData ExeproxData, fingerprint string) (sshKeyData, bool, error) {
	data, ok := skd.sshKeys.Load(fingerprint)
	if ok {
		return data, true, nil
	}

	data, exists, err := exeproxData.SSHKeyByFingerprint(ctx, fingerprint)

	if err == nil && exists {
		skd.sshKeys.Store(fingerprint, data)
	}

	return data, exists, err
}

// clear clears the SSH keys cache.
func (skd *sshKeysData) clear() {
	skd.sshKeys.Clear()
}

// deleteSSHKey deletes information about an SSH key.
// This is called when we receive a notification from exed
// about a deleted SSH key.
func (skd *sshKeysData) deleteSSHKey(ctx context.Context, fingerprint string) {
	skd.sshKeys.Delete(fingerprint)
}
