package execore

import (
	"context"
	"net/http"

	"exe.dev/apns"
)

// handleVMPushSend handles POST /_/gateway/push/send from the metadata proxy.
func (s *Server) handleVMPushSend(w http.ResponseWriter, r *http.Request) {
	s.proxyServer().HandleVMPushSend(w, r)
}

// apnsPushSender wraps an [apns.Client] to implement [exeweb.PushSender].
type apnsPushSender struct {
	client *apns.Client
}

func (s *apnsPushSender) Send(ctx context.Context, deviceToken, title, body string, data map[string]string) error {
	return s.client.Send(ctx, deviceToken, title, body, data)
}
