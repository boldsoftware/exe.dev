package llmgateway

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"exe.dev/accounting"
)

// llmGateway is a proxy for API calls to various LLM services.
// - Authenticates client requests to verify that they are coming from known box names.
// - Performs account balance checks before allowing requests to continue.
// - Debits an associated billing account with the cost of handling the API call
// - Designed to work with client applications that have configurable API endpoints and auth headers.
//
// TODO:
// handler for /{modelAlias}[/...] routes, to actually proxy the client requests.
type llmGateway struct {
	now             func() time.Time
	mux             http.ServeMux
	accountant      accounting.Accountant
	boxKeyAuthority boxKeyAuthority
}

type boxKeyAuthority interface {
	// SSHIdentityKeyForBox returns the public key portion of the ssh server identity for the given boxy name.
	SSHIdentityKeyForBox(ctx context.Context, name string) (string, error)
}

func NewGateway(accountant accounting.Accountant, boxKeyAuthority boxKeyAuthority) *llmGateway {
	ret := &llmGateway{
		now:             time.Now,
		mux:             *http.NewServeMux(),
		accountant:      accountant,
		boxKeyAuthority: boxKeyAuthority,
	}

	ret.mux.HandleFunc("/{modelAlias}", ret.handleRequest)
	return ret
}

func (m *llmGateway) handleRequest(w http.ResponseWriter, r *http.Request) {
	alias := r.PathValue("modelAlias")
	slog.Debug("llmGateway.handleRequest", "alias", alias)

	// authenticate request
	billingAccountID, err := m.boxKeyAuth(r.Context(), r)
	if err != nil {
		httpError(w, r, "box key auth failed: "+err.Error(), http.StatusUnauthorized)
		return
	}

	balance, err := m.accountant.GetUserBalance(r.Context(), billingAccountID)
	if err != nil {
		httpError(w, r, "unable to check account ballance: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if balance < 0.0 {
		httpError(w, r, "insufficient account balance", http.StatusPaymentRequired)
		return
	}

	httpError(w, r, "actual proxy methods not yet implemented", http.StatusNotImplemented)
}

// ServeHTTP implements http.Handler
func (m *llmGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mux.ServeHTTP(w, r)
}

func httpError(w http.ResponseWriter, r *http.Request, errstr string, code int) {
	http.Error(w, errstr, code)
	slog.Error("llmgateway.httpError", "method", r.Method, "path", r.URL.Path, "code", code, "error", errstr)
}
