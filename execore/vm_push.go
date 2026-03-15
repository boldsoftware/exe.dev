package execore

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"exe.dev/apns"
	"exe.dev/exeweb"
)

// handleVMPushSend handles POST /_/gateway/push/send from the metadata proxy.
func (s *Server) handleVMPushSend(w http.ResponseWriter, r *http.Request) {
	s.proxyServer().HandleVMPushSend(w, r)
}

// apnsPushSender wraps an [apns.Client] to implement [exeweb.PushSender],
// translating APNs-specific errors to the generic push errors.
type apnsPushSender struct {
	client *apns.Client
}

func (s *apnsPushSender) Send(ctx context.Context, deviceToken, title, body string, data map[string]string) error {
	err := s.client.Send(ctx, deviceToken, title, body, data)
	if errors.Is(err, apns.ErrTokenInvalid) {
		return fmt.Errorf("%w", exeweb.ErrPushTokenInvalid)
	}
	return err
}
