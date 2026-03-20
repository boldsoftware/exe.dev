package execore

import (
	"context"
	"fmt"
	"net/http"

	"exe.dev/apns"
)

// handleVMPushSend handles POST /_/gateway/push/send from the metadata proxy.
func (s *Server) handleVMPushSend(w http.ResponseWriter, r *http.Request) {
	s.proxyServer().HandleVMPushSend(w, r)
}

// apnsPushSender wraps APNs clients for production and sandbox environments
// to implement [exeweb.PushSender].
type apnsPushSender struct {
	production *apns.Client
	sandbox    *apns.Client
}

func (s *apnsPushSender) Send(ctx context.Context, environment, deviceToken, title, body string, data map[string]string) error {
	var client *apns.Client
	switch environment {
	case "sandbox":
		client = s.sandbox
	default:
		client = s.production
	}
	if client == nil {
		return fmt.Errorf("APNs %s client not configured", environment)
	}
	return client.Send(ctx, deviceToken, title, body, data)
}
