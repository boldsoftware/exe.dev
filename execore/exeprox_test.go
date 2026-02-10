package execore

import (
	"io"
	"sync"
	"testing"

	proxyapi "exe.dev/pkg/api/exe/proxy/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// Test that changes are reported via the Changes grpc call.
func TestProxyChanges(t *testing.T) {
	// This test can't run in parallel because other tests
	// may cause values to be sent on the changes stream.

	var wg sync.WaitGroup
	defer wg.Wait()

	s := newTestServer(t)

	conn, err := grpc.NewClient(s.exeproxServiceLn.addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	client := proxyapi.NewProxyInfoServiceClient(conn)

	ch := make(chan *proxyapi.ChangesResponse, 1)

	done := make(chan bool)
	defer close(done)

	defer func() {
		if err := stopProxyChangesStream(); err != nil {
			t.Error(err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()

		stream, err := client.Changes(t.Context(), &proxyapi.ChangesRequest{})
		if err != nil {
			t.Error(err)
			return
		}

		for {
			resp, err := stream.Recv()
			if err == io.EOF || status.Code(err) == codes.Canceled {
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			select {
			case ch <- resp:
			case <-done:
				return
			case <-t.Context().Done():
				return
			}
		}
	}()

	const fakeUserID = "fake-user-id"
	const fakeDomain = "fake-domain"

	addCookie := func() string {
		cookieValue, err := s.createAuthCookie(t.Context(), fakeUserID, fakeDomain)
		if err != nil {
			t.Fatal(err)
		}
		return cookieValue
	}

	deleteCookie := func(cookieValue string) {
		s.deleteAuthCookie(t.Context(), cookieValue)
	}

	testDeleteCookie := func(t *testing.T, cookieValue string) {
		t.Helper()
		select {
		case change := <-ch:
			acd, ok := change.Action.(*proxyapi.ChangesResponse_DeletedCookie)
			if !ok {
				t.Fatalf("got change type %T, want %T", change.Action, (*proxyapi.ChangesResponse_DeletedCookie)(nil))
			}
			key := acd.DeletedCookie.Key
			val, ok := key.(*proxyapi.DeletedCookie_CookieValue)
			if !ok {
				t.Fatalf("got deleted cookie key type %T, want %T", key, (*proxyapi.DeletedCookie_CookieValue)(nil))
			}
			if val.CookieValue != cookieValue {
				t.Errorf("got cookie value %q, want %q", val.CookieValue, cookieValue)
			}
		case <-t.Context().Done():
			t.Error("did not see expected deleted cookie")
		}
	}

	// This is not a comprehensive test, which is for e1e.
	// This just tests that streaming works as expected.

	cookieValue := addCookie()
	deleteCookie(cookieValue)
	t.Run("deleted-cookie-1", func(t *testing.T) { testDeleteCookie(t, cookieValue) })

	cookieValue = addCookie()
	deleteCookie(cookieValue)
	t.Run("deleted-cookie-2", func(t *testing.T) { testDeleteCookie(t, cookieValue) })
}
