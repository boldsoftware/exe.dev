package exeweb

import (
	"context"
)

// BoxAccessType represents the type of access a user has to a box
type BoxAccessType int

const (
	BoxAccessNone BoxAccessType = iota
	BoxAccessOwner
	BoxAccessEmailShare
	BoxAccessShareLink
	BoxAccessTeamShare
	BoxAccessPublic
)

// HasUserAccessToBox checks what type of access a user has to a box.
func (ps *ProxyServer) HasUserAccessToBox(ctx context.Context, userID string, box *BoxData) (BoxAccessType, error) {
	// Check if user is owner
	if box.CreatedByUserID == userID {
		return BoxAccessOwner, nil
	}

	hasAccess, err := ps.Data.HasUserAccessToBox(ctx, box.ID, box.Name, userID)
	if err != nil {
		return BoxAccessNone, err
	}
	if hasAccess {
		return BoxAccessEmailShare, nil
	}

	// Check if box is shared with user's team.
	isTeamShared, err := ps.Data.IsBoxSharedWithUserTeam(ctx, box.ID, box.Name, userID)
	if err != nil {
		return BoxAccessNone, err
	}
	if isTeamShared {
		return BoxAccessTeamShare, nil
	}

	// Check if box is public -
	// any authenticated user can access public boxes.
	if box.BoxRoute.Share == "public" {
		return BoxAccessPublic, nil
	}

	return BoxAccessNone, nil
}
