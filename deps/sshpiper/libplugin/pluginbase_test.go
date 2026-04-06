package libplugin

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestAuthDenialErrorTranslation verifies that the libplugin server's auth
// dispatchers translate an *AuthDenialError returned by a callback into a
// Denial proto field on the response, instead of propagating it as a gRPC
// error. This is the contract sshpiperd's grpc plugin client relies on.
func TestAuthDenialErrorTranslation(t *testing.T) {
	denialErr := &AuthDenialError{
		Banner: "VM is not running\n",
		Reason: "vm is stopped",
	}
	wrappedErr := fmt.Errorf("wrapping: %w", denialErr)

	tests := []struct {
		name string
		// run dispatches the callback through one of the four auth methods
		// on the libplugin server and returns the AuthDenial that landed in
		// the response (or nil if none).
		run func(t *testing.T, returnErr error) *AuthDenial
	}{
		{
			name: "NoneAuth",
			run: func(t *testing.T, returnErr error) *AuthDenial {
				s := &server{config: SshPiperPluginConfig{
					NoClientAuthCallback: func(ConnMetadata) (*Upstream, error) {
						return nil, returnErr
					},
				}}
				resp, err := s.NoneAuth(context.Background(), &NoneAuthRequest{Meta: &ConnMeta{}})
				if err != nil {
					t.Fatalf("NoneAuth: unexpected error: %v", err)
				}
				return resp.GetDenial()
			},
		},
		{
			name: "PasswordAuth",
			run: func(t *testing.T, returnErr error) *AuthDenial {
				s := &server{config: SshPiperPluginConfig{
					PasswordCallback: func(ConnMetadata, []byte) (*Upstream, error) {
						return nil, returnErr
					},
				}}
				resp, err := s.PasswordAuth(context.Background(), &PasswordAuthRequest{Meta: &ConnMeta{}})
				if err != nil {
					t.Fatalf("PasswordAuth: unexpected error: %v", err)
				}
				return resp.GetDenial()
			},
		},
		{
			name: "PublicKeyAuth",
			run: func(t *testing.T, returnErr error) *AuthDenial {
				s := &server{config: SshPiperPluginConfig{
					PublicKeyCallback: func(ConnMetadata, []byte) (*Upstream, error) {
						return nil, returnErr
					},
				}}
				resp, err := s.PublicKeyAuth(context.Background(), &PublicKeyAuthRequest{Meta: &ConnMeta{}})
				if err != nil {
					t.Fatalf("PublicKeyAuth: unexpected error: %v", err)
				}
				return resp.GetDenial()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+"/direct", func(t *testing.T) {
			denial := tt.run(t, denialErr)
			if denial == nil {
				t.Fatal("expected denial in response, got nil")
			}
			if denial.GetBanner() != denialErr.Banner {
				t.Errorf("banner mismatch: got %q, want %q", denial.GetBanner(), denialErr.Banner)
			}
			if denial.GetReason() != denialErr.Reason {
				t.Errorf("reason mismatch: got %q, want %q", denial.GetReason(), denialErr.Reason)
			}
		})

		// errors.As should unwrap, so a wrapped denial still translates.
		t.Run(tt.name+"/wrapped", func(t *testing.T) {
			denial := tt.run(t, wrappedErr)
			if denial == nil {
				t.Fatal("expected denial in response from wrapped error, got nil")
			}
			if denial.GetBanner() != denialErr.Banner {
				t.Errorf("banner mismatch: got %q, want %q", denial.GetBanner(), denialErr.Banner)
			}
		})
	}
}

// TestAuthDenialErrorPropagatesGenericErrors verifies that callbacks returning
// a non-AuthDenialError get their error propagated as a gRPC error (and not
// silently translated into a denial).
func TestAuthDenialErrorPropagatesGenericErrors(t *testing.T) {
	plain := errors.New("something exploded")
	s := &server{config: SshPiperPluginConfig{
		PublicKeyCallback: func(ConnMetadata, []byte) (*Upstream, error) {
			return nil, plain
		},
	}}
	resp, err := s.PublicKeyAuth(context.Background(), &PublicKeyAuthRequest{Meta: &ConnMeta{}})
	if err == nil {
		t.Fatalf("expected error, got response %+v", resp)
	}
	if !errors.Is(err, plain) {
		t.Errorf("expected wrapped %v, got %v", plain, err)
	}
}

// TestDenyHelper verifies the Deny convenience constructor returns an
// *AuthDenialError populated with the supplied banner and reason, so plugins
// can use the helper interchangeably with constructing the type directly.
func TestDenyHelper(t *testing.T) {
	err := Deny("hello banner", "hello reason")
	var de *AuthDenialError
	if !errors.As(err, &de) {
		t.Fatalf("Deny() returned %T, want *AuthDenialError (or wrapper)", err)
	}
	if de.Banner != "hello banner" {
		t.Errorf("Banner = %q, want %q", de.Banner, "hello banner")
	}
	if de.Reason != "hello reason" {
		t.Errorf("Reason = %q, want %q", de.Reason, "hello reason")
	}
}

// TestAuthDenialErrorErrorMethod verifies the Error() method returns the
// reason when set (errors.As callers log the reason, not the banner) and
// falls back to a non-empty default when Reason is empty so the error is
// never the empty string.
func TestAuthDenialErrorErrorMethod(t *testing.T) {
	t.Run("withReason", func(t *testing.T) {
		de := &AuthDenialError{Banner: "user-visible banner", Reason: "internal reason"}
		if got := de.Error(); got != "internal reason" {
			t.Errorf("AuthDenialError.Error() = %q, want %q", got, "internal reason")
		}
	})
	t.Run("emptyReasonFallback", func(t *testing.T) {
		de := &AuthDenialError{Banner: "user-visible banner"}
		if got := de.Error(); got == "" {
			t.Error("AuthDenialError.Error() returned empty string for empty Reason; want non-empty fallback")
		}
	})
}
