package execore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	crand "crypto/rand"

	"exe.dev/exedb"
)

// Box Sharing Model
//
// exe.dev boxes support sharing HTTPS proxy access with other users via two mechanisms:
//
// 1. Email-based sharing: Share with specific users by email address.
//    - When sharing with an email, we create a pending_box_shares entry
//    - When the user registers/logs in, pending shares are converted to active box_shares
//    - Users with active shares can access the box's HTTPS proxy
//    - Email sending is rate-limited to 100 emails per user per day
//
// 2. Share links: Create anonymous shareable URLs with tokens.
//    - Anyone with the link can access after authenticating
//    - Tokens are generated using crypto/rand.Text()
//    - Usage is tracked (use_count, last_used_at)
//    - Links can be revoked at any time
//    - this is sort of the "discord" model in that after you use the link,
//      you create an account, and that account is added to the shares.
//
// Access Control:
//   - All boxes start as "private" (owner-only access)
//   - When a request comes in, we check (in order):
//     1. Is the user the box owner? → Allow
//     2. Does the user have an active email-based share? → Allow
//     3. Does the request include a valid share link token? → Allow
//        - On first access via share link, an email-based share is auto-created
//        - This allows the user to retain access even if the share link is revoked
//     4. Otherwise → Deny (404 to avoid leaking box existence)
//
// The "public" mode is separate and bypasses all authentication.
//
// Database schema:
//   - pending_box_shares: Email invitations before user registration
//   - box_shares: Active shares with registered users
//   - box_share_links: Anonymous shareable links
//   - user_daily_email_counts: Rate limiting tracker

// SharedBoxDisplayInfo represents a box shared with the user
type SharedBoxDisplayInfo struct {
	Name       string
	OwnerEmail string
	ProxyURL   string
}

// BoxAccessType represents the type of access a user has to a box
type BoxAccessType int

const (
	BoxAccessNone BoxAccessType = iota
	BoxAccessOwner
	BoxAccessEmailShare
	BoxAccessShareLink
	BoxAccessPublic
)

// generateShareToken generates a cryptographically secure random token for share links
func generateShareToken() string {
	return crand.Text()
}

// checkAndIncrementEmailQuota checks if user is under their daily email limit and increments if so
func (s *Server) checkAndIncrementEmailQuota(ctx context.Context, userID string) error {
	today := time.Now().UTC().Format("2006-01-02")

	count, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserEmailCountForDate, exedb.GetUserEmailCountForDateParams{
		UserID: userID,
		Date:   today,
	})
	// If no row exists, count will be 0
	if errors.Is(err, sql.ErrNoRows) {
		count = 0
		err = nil
	}
	if err != nil {
		return fmt.Errorf("failed to check email quota: %w", err)
	}

	if count >= 100 {
		return fmt.Errorf("daily email limit reached (100/day)")
	}

	// Increment counter
	return withTx1(s, ctx, (*exedb.Queries).IncrementUserEmailCount, exedb.IncrementUserEmailCountParams{
		UserID: userID,
		Date:   today,
	})
}

// hasUserAccessToBox checks what type of access a user has to a box
func (s *Server) hasUserAccessToBox(ctx context.Context, userID string, box *exedb.Box) (BoxAccessType, error) {
	// Check if user is owner
	if box.CreatedByUserID == userID {
		return BoxAccessOwner, nil
	}

	// Try to resolve any pending shares for this user before checking access.
	// This is a defensive measure to catch any edge cases where pending shares
	// weren't resolved during login (e.g., if we miss a login path in the future).
	user, err := withRxRes1(s, ctx, (*exedb.Queries).GetUserWithDetails, userID)
	if err == nil && user.Email != "" {
		if err := s.resolvePendingShares(ctx, user.Email, userID); err != nil {
			return BoxAccessNone, fmt.Errorf("resolve pending shares: %w", err)
		}
	}

	// Check if user has share access
	hasAccess, err := withRxRes1(s, ctx, (*exedb.Queries).HasUserAccessToBox, exedb.HasUserAccessToBoxParams{
		BoxID:            int64(box.ID),
		SharedWithUserID: userID,
	})
	if err != nil {
		return BoxAccessNone, err
	}

	if hasAccess {
		return BoxAccessEmailShare, nil
	}

	// Check if box is public - any authenticated user can access public boxes
	route := box.GetRoute()
	if route.Share == "public" {
		return BoxAccessPublic, nil
	}

	return BoxAccessNone, nil
}

// validateShareLinkForBox checks if a share token is valid for a given box
func (s *Server) validateShareLinkForBox(ctx context.Context, shareToken string, boxID int) (bool, error) {
	link, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxShareLinkByTokenAndBoxID, exedb.GetBoxShareLinkByTokenAndBoxIDParams{
		ShareToken: shareToken,
		BoxID:      int64(boxID),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	return link.ShareToken == shareToken, nil
}

// countTotalShares returns the total number of shares (pending + active) and links for a box
func (s *Server) countTotalShares(ctx context.Context, boxID int) (userShares, linkShares int64, err error) {
	err = s.withRx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		pendingCount, err := queries.CountPendingBoxShares(ctx, int64(boxID))
		if err != nil {
			return err
		}

		activeCount, err := queries.CountBoxShares(ctx, int64(boxID))
		if err != nil {
			return err
		}

		linkCount, err := queries.CountBoxShareLinks(ctx, int64(boxID))
		if err != nil {
			return err
		}

		userShares = pendingCount + activeCount
		linkShares = linkCount
		return nil
	})
	return userShares, linkShares, err
}

// getShareLinks returns a list of share links with their full URLs
func (s *Server) getShareLinks(ctx context.Context, boxID int, boxName, userID string) []BoxShareLinkInfo {
	var links []BoxShareLinkInfo

	shareLinks, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxShareLinksByBoxID, exedb.GetBoxShareLinksByBoxIDParams{
		BoxID:           int64(boxID),
		CreatedByUserID: userID,
	})
	if err == nil {
		for _, sl := range shareLinks {
			links = append(links, BoxShareLinkInfo{
				Token: sl.ShareToken,
				URL:   fmt.Sprintf("%s?share=%s", s.boxProxyAddress(boxName), sl.ShareToken),
			})
		}
	}

	return links
}

// getSharedEmails returns a list of emails that a box is shared with
func (s *Server) getSharedEmails(ctx context.Context, boxID int) []string {
	var emails []string

	// Get pending shares
	pendingShares, err := withRxRes1(s, ctx, (*exedb.Queries).GetPendingBoxSharesByBoxID, int64(boxID))
	if err == nil {
		for _, ps := range pendingShares {
			emails = append(emails, ps.SharedWithEmail)
		}
	}

	// Get active shares
	activeShares, err := withRxRes1(s, ctx, (*exedb.Queries).GetBoxSharesByBoxID, int64(boxID))
	if err == nil {
		for _, as := range activeShares {
			emails = append(emails, as.SharedWithUserEmail)
		}
	}

	return emails
}

// resolvePendingShares converts pending shares to active shares when a user registers
func (s *Server) resolvePendingShares(ctx context.Context, email, userID string) error {
	// Get pending shares for this email
	pendingShares, err := withRxRes1(s, ctx, (*exedb.Queries).GetPendingBoxSharesByEmail, email)
	if err != nil {
		return err
	}

	if len(pendingShares) == 0 {
		return nil
	}

	// Convert each pending share to an active share
	return s.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		for _, ps := range pendingShares {
			// Create active share
			_, err := queries.CreateBoxShare(ctx, exedb.CreateBoxShareParams{
				BoxID:            ps.BoxID,
				SharedWithUserID: userID,
				SharedByUserID:   ps.SharedByUserID,
				Message:          ps.Message,
			})
			if err != nil {
				// If it already exists (duplicate), skip it
				if !strings.Contains(err.Error(), "UNIQUE constraint") {
					return err
				}
			}

			// Delete the pending share
			err = queries.DeletePendingBoxShareByBoxAndEmail(ctx, exedb.DeletePendingBoxShareByBoxAndEmailParams{
				BoxID:           ps.BoxID,
				SharedWithEmail: email,
			})
			if err != nil {
				return err
			}

			s.slog().InfoContext(ctx, "resolved pending share", "user_id", userID, "box", ps.BoxName)
		}
		return nil
	})
}
