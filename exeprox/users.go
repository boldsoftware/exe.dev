package exeprox

import (
	"context"

	"github.com/go4org/hashtriemap"
)

// usersData holds information about users.
type usersData struct {
	users hashtriemap.HashTrieMap[string, userData]
}

// userData holds information about a user.
type userData struct {
	userID      string
	email       string
	rootSupport int64  // level of root support
	isLockedOut bool   // whether user is locked out
	accountID   string // accounting ID from accounts database table
}

// lookup returns information about a user given the user ID.
// The bool result reports whether the user exists.
func (ud *usersData) lookup(ctx context.Context, exeproxData ExeproxData, userID string) (userData, bool, error) {
	data, ok := ud.users.Load(userID)
	if ok {
		return data, true, nil
	}

	data, exists, err := exeproxData.UserInfo(ctx, userID)

	if err == nil && exists {
		ud.users.Store(userID, data)
	}

	return data, exists, err
}

// clear clears the users cache.
func (ud *usersData) clear() {
	ud.users.Clear()
}

// deleteUser removes user information from the cache.
func (ud *usersData) deleteUser(ctx context.Context, userID string) {
	ud.users.Delete(userID)
}
