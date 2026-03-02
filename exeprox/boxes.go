package exeprox

import (
	"context"

	"exe.dev/exeweb"

	"github.com/go4org/hashtriemap"
)

// boxesData holds information about boxes.
type boxesData struct {
	// boxes is a map from box name to box data.
	boxes hashtriemap.HashTrieMap[string, exeweb.BoxData]
	// boxShares is a set of pairs of share token and user ID.
	boxShares hashtriemap.HashTrieMap[boxShare, struct{}]
	// shareLinks is a map from share token to box share link.
	boxShareLinks hashtriemap.HashTrieMap[string, boxShareLink]
	// boxTeamShares is a set of pairs of box name and user ID,
	// where the box has been shared with the user's team.
	// We don't try to keep track of general team membership.
	boxTeamShares hashtriemap.HashTrieMap[boxShare, struct{}]
	// boxShelleyShares is a set of pairs of box name and user ID,
	// where the box has team_shelley sharing enabled and the user
	// is in the same team as the box creator.
	boxShelleyShares hashtriemap.HashTrieMap[boxShare, struct{}]
}

// boxShare is a share of a box with a user.
type boxShare struct {
	boxName string
	userID  string
}

// boxShareLinks holds information about a box share link.
type boxShareLink struct {
	boxName string
}

// lookup returns the information about a box given its name.
// The bool result reports whether the box exists.
func (bd *boxesData) lookup(ctx context.Context, exeproxData ExeproxData, boxName string) (exeweb.BoxData, bool, error) {
	data, ok := bd.boxes.Load(boxName)
	if ok {
		return data, true, nil
	}

	data, exists, err := exeproxData.BoxInfo(ctx, boxName)

	if err == nil && exists {
		bd.boxes.Store(boxName, data)
	}

	return data, exists, err
}

// isSharedWithUser reports whether a box has been shared with a user.
func (bd *boxesData) isSharedWithUser(ctx context.Context, exeproxData ExeproxData, boxID int, boxName, sharedWithUserID string) (bool, error) {
	// Look in the cache.
	key := boxShare{
		boxName: boxName,
		userID:  sharedWithUserID,
	}
	_, ok := bd.boxShares.Load(key)
	if ok {
		return ok, nil
	}

	// Not found in cache. Ask exed whether user has access.
	ok, err := exeproxData.HasUserAccessToBox(ctx, boxID, boxName, sharedWithUserID)
	if err != nil {
		return false, err
	}

	if ok {
		// Access granted; cache the result.
		bd.boxShares.Store(key, struct{}{})
	}

	return ok, nil
}

// isSharedWithUserTeam reports whether a box has been shared with
// a team that a user is a member of.
func (bd *boxesData) isSharedWithUserTeam(ctx context.Context, exeproxData ExeproxData, boxID int, boxName, sharedWithUserID string) (bool, error) {
	// Look in the cache.
	key := boxShare{
		boxName: boxName,
		userID:  sharedWithUserID,
	}
	_, ok := bd.boxTeamShares.Load(key)
	if ok {
		return ok, nil
	}

	// Not found in cache. Ask exed whether user is in a team
	// with access.
	ok, err := exeproxData.IsBoxSharedWithUserTeam(ctx, boxID, boxName, sharedWithUserID)
	if err != nil {
		return false, err
	}

	if ok {
		// Access granted; cache the result.
		bd.boxTeamShares.Store(key, struct{}{})
	}

	return ok, nil
}

// isShelleySharedWithTeamMember reports whether a box has team_shelley
// sharing enabled and the user is in the same team as the box creator.
func (bd *boxesData) isShelleySharedWithTeamMember(ctx context.Context, exeproxData ExeproxData, boxID int, boxName, userID string) (bool, error) {
	key := boxShare{
		boxName: boxName,
		userID:  userID,
	}
	_, ok := bd.boxShelleyShares.Load(key)
	if ok {
		return ok, nil
	}

	ok, err := exeproxData.IsBoxShelleySharedWithTeamMember(ctx, boxID, boxName, userID)
	if err != nil {
		return false, err
	}

	if ok {
		bd.boxShelleyShares.Store(key, struct{}{})
	}

	return ok, nil
}

// isShareLinkValid reports whether a share link is valid for a box.
func (bd *boxesData) isShareLinkValid(ctx context.Context, exeproxData ExeproxData, boxID int, boxName, userID, shareToken string) (bool, error) {
	// Look in the cache.
	bsl, ok := bd.boxShareLinks.Load(shareToken)
	if ok {
		return bsl.boxName == boxName, nil
	}

	// Not found in cache. Ask exed whether the share token is valid.
	// This will also record that the share token was used,
	// and create an email-based share for the user.
	ok, err := exeproxData.CheckShareLink(ctx, boxID, boxName, userID, shareToken)
	if err != nil {
		return false, err
	}

	if ok {
		// Access granted; cache the result.
		bsl := boxShareLink{
			boxName: boxName,
		}
		bd.boxShareLinks.Store(shareToken, bsl)
	}

	return ok, nil
}

// clear clears the boxes cache.
func (bd *boxesData) clear() {
	bd.boxes.Clear()
	bd.boxShares.Clear()
	bd.boxShareLinks.Clear()
	bd.boxTeamShares.Clear()
	bd.boxShelleyShares.Clear()
}

// deleteBox deletes information about a box.
// This is called when we receive a notification from exed
// about a deleted box.
func (bd *boxesData) deleteBox(ctx context.Context, boxName string) {
	bd.boxes.Delete(boxName)

	for share := range bd.boxShares.All() {
		if share.boxName == boxName {
			bd.boxShares.Delete(share)
		}
	}

	for token, share := range bd.boxShareLinks.All() {
		if share.boxName == boxName {
			bd.boxShareLinks.Delete(token)
		}
	}

	for share := range bd.boxTeamShares.All() {
		if share.boxName == boxName {
			bd.boxTeamShares.Delete(share)
		}
	}

	for share := range bd.boxShelleyShares.All() {
		if share.boxName == boxName {
			bd.boxShelleyShares.Delete(share)
		}
	}
}

// renameBox is called when we receive a notification from exed
// about a renamed box. We just drop the information and refetch it
// if we need it.
func (bd *boxesData) renameBox(ctx context.Context, oldBoxName, newBoxName string) {
	bd.deleteBox(ctx, oldBoxName)
}

// updateBoxRoute is called when we update a box route.
// We just drop the information and refetch it if we need it.
func (bd *boxesData) updateBoxRoute(ctx context.Context, boxName string, boxRoute exeweb.BoxRoute) {
	bd.deleteBox(ctx, boxName)
}

// deleteBoxShare is called when we receive a notification from exed
// that a box share has been deleted.
func (bd *boxesData) deleteBoxShare(ctx context.Context, boxName, sharedWithUserID string) {
	key := boxShare{
		boxName: boxName,
		userID:  sharedWithUserID,
	}
	bd.boxShares.Delete(key)
}

// deleteBoxShareLink is called when we receive a notification from exed
// that a box share link has been deleted.
func (bd *boxesData) deleteBoxShareLink(ctx context.Context, boxName, shareToken string) {
	bd.boxShareLinks.Delete(shareToken)
}

// deleteTeamUser is called when we receive a notification that
// a user has been deleted from a team.
// We don't keep track of teams, so we just discard all information
// about that user from the team shares. We will recache as needed.
func (bd *boxesData) deleteTeamUser(ctx context.Context, userID string) {
	for share := range bd.boxTeamShares.All() {
		if share.userID == userID {
			bd.boxTeamShares.Delete(share)
		}
	}
	for share := range bd.boxShelleyShares.All() {
		if share.userID == userID {
			bd.boxShelleyShares.Delete(share)
		}
	}
}

// deleteBoxShareTeam is called when we receive a notification
// that a box is no longer shared with a team.
// We don't keep track of teams, so we just discard all information
// about that box from the team shares. We will recache as needed.
func (bd *boxesData) deleteBoxShareTeam(ctx context.Context, boxName string) {
	for share := range bd.boxTeamShares.All() {
		if share.boxName == boxName {
			bd.boxTeamShares.Delete(share)
		}
	}
}
