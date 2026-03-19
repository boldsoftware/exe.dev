package exeprox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"exe.dev/exeweb"
	proxyapi "exe.dev/pkg/api/exe/proxy/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// watchChanges runs in its own goroutine.
// It listens for changes reported by exed.
// We use them to update our internal caches.
func (p *Proxy) watchChanges() {
	if p.grpcClient == nil {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for {
		if p.stopping.Load() {
			// exeprox is stopping.
			return
		}

		stream, err := p.grpcClient.Changes(ctx, &proxyapi.ChangesRequest{})
		if err != nil {
			p.lg.ErrorContext(ctx, "requesting proxy changes failed", "error", err)
			time.Sleep(10 * time.Second)
			continue
		}

		p.processChanges(ctx, stream)

		// Something went wrong with the stream of changes.
		// Clear all caches and start again.

		p.boxes.clear()
		p.cookies.clear()
		p.users.clear()
		p.sshKeys.clear()
		p.exe1Tokens.clear()
	}
}

// processChanges reads from the stream of changes.
func (p *Proxy) processChanges(ctx context.Context, stream proxyapi.ProxyInfoService_ChangesClient) {
	for {
		resp, err := stream.Recv()
		if err != nil {
			isEOF := false
			if errors.Is(err, io.EOF) {
				isEOF = true
			} else {
				switch status.Code(err) {
				case codes.Canceled, codes.Unavailable:
					isEOF = true
				}
			}

			if isEOF {
				p.lg.InfoContext(ctx, "EOF reading proxy change", "error", err)
			} else {
				p.lg.ErrorContext(ctx, "failure reading proxy change", "error", err)
			}

			// Return to reopen connection.
			return
		}

		p.processChange(ctx, resp)
	}
}

// processChange handles a single proxy change.
func (p *Proxy) processChange(ctx context.Context, change *proxyapi.ChangesResponse) {
	switch action := change.Action.(type) {
	case *proxyapi.ChangesResponse_DeletedBox:
		p.dropBoxSSHConns(action.DeletedBox.BoxName)
		p.boxes.deleteBox(ctx, action.DeletedBox.BoxName)
	case *proxyapi.ChangesResponse_RenamedBox:
		p.boxes.renameBox(ctx, action.RenamedBox.OldBoxName, action.RenamedBox.NewBoxName)
	case *proxyapi.ChangesResponse_UpdatedBoxRoute:
		var r exeweb.BoxRoute
		if action.UpdatedBoxRoute.Route != nil {
			r = exeweb.BoxRoute{
				Port:  int(action.UpdatedBoxRoute.Route.Port),
				Share: action.UpdatedBoxRoute.Route.Share,
			}
		}
		p.boxes.updateBoxRoute(ctx, action.UpdatedBoxRoute.BoxName, r)
	case *proxyapi.ChangesResponse_DeletedCookie:
		p.processDeletedCookieChange(ctx, action.DeletedCookie)
	case *proxyapi.ChangesResponse_DeletedBoxShare:
		p.boxes.deleteBoxShare(ctx, action.DeletedBoxShare.BoxName, action.DeletedBoxShare.SharedWithUserID)
	case *proxyapi.ChangesResponse_DeletedBoxShareLink:
		p.boxes.deleteBoxShareLink(ctx, action.DeletedBoxShareLink.BoxName, action.DeletedBoxShareLink.ShareToken)
	case *proxyapi.ChangesResponse_DeletedSSHKey:
		p.sshKeys.deleteSSHKey(ctx, action.DeletedSSHKey.Fingerprint)
	case *proxyapi.ChangesResponse_UserChanged:
		// Just delete user information and reload if needed.
		p.users.deleteUser(ctx, action.UserChanged.UserInfo.UserID)
	case *proxyapi.ChangesResponse_DeletedTeamMember:
		p.boxes.deleteTeamUser(ctx, action.DeletedTeamMember.UserID)
	case *proxyapi.ChangesResponse_DeletedBoxShareTeam:
		p.boxes.deleteBoxShareTeam(ctx, action.DeletedBoxShareTeam.BoxName)
	case *proxyapi.ChangesResponse_MovedBox:
		p.dropBoxSSHConns(action.MovedBox.BoxName)
		p.boxes.movedBox(ctx, action.MovedBox.BoxName)
	default:
		p.lg.ErrorContext(ctx, "unknown type processing proxy change", "type", fmt.Sprintf("%T", change.Action))
	}
}

// dropBoxSSHConns drops pooled SSH connections for a box, if its routing
// info is cached. Must be called before deleting the box from the cache.
func (p *Proxy) dropBoxSSHConns(boxName string) {
	data, ok := p.boxes.boxes.Load(boxName)
	if !ok || data.SSHPort == 0 {
		return
	}
	host := exeweb.BoxSSHHost(p.lg, data.Ctrhost)
	p.sshPool.DropConnectionsTo(host, data.SSHPort)
}

// processDeletedCookieChange handles a deleted cookie.
func (p *Proxy) processDeletedCookieChange(ctx context.Context, deletedCookie *proxyapi.DeletedCookie) {
	switch key := deletedCookie.Key.(type) {
	case *proxyapi.DeletedCookie_CookieValue:
		p.cookies.deleteCookie(ctx, key.CookieValue)
	case *proxyapi.DeletedCookie_UserID:
		p.cookies.deleteCookiesForUser(ctx, key.UserID)
	default:
		p.lg.ErrorContext(ctx, "unknown type processing proxy deleted cookie change", "type", fmt.Sprintf("%T", deletedCookie.Key))
	}
}
