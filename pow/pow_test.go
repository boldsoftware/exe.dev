package pow

import (
	"testing"
	"time"
)

func TestChallengerBasic(t *testing.T) {
	c := NewChallenger([]byte("test-secret"), 8, time.Minute)

	token, err := c.NewToken()
	if err != nil {
		t.Fatalf("NewToken failed: %v", err)
	}

	// Solve the challenge
	nonce := Solve(token, 8)

	// Verify should pass
	if err := c.Verify(token, nonce); err != nil {
		t.Errorf("Verify failed with valid nonce: %v", err)
	}
}

func TestChallengerWrongNonce(t *testing.T) {
	c := NewChallenger([]byte("test-secret"), 16, time.Minute)

	token, err := c.NewToken()
	if err != nil {
		t.Fatalf("NewToken failed: %v", err)
	}

	// Use a wrong nonce
	if err := c.Verify(token, 12345); err != ErrInvalidProof {
		t.Errorf("expected ErrInvalidProof, got %v", err)
	}
}

func TestChallengerExpired(t *testing.T) {
	c := NewChallenger([]byte("test-secret"), 8, -time.Second) // Already expired

	token, err := c.NewToken()
	if err != nil {
		t.Fatalf("NewToken failed: %v", err)
	}

	nonce := Solve(token, 8)

	if err := c.Verify(token, nonce); err != ErrTokenExpired {
		t.Errorf("expected ErrTokenExpired, got %v", err)
	}
}

func TestChallengerTamperedToken(t *testing.T) {
	c := NewChallenger([]byte("test-secret"), 8, time.Minute)

	token, err := c.NewToken()
	if err != nil {
		t.Fatalf("NewToken failed: %v", err)
	}

	// Tamper with the token (change a character in the payload)
	tampered := "X" + token[1:]

	if err := c.Verify(tampered, 0); err != ErrInvalidToken && err != ErrMalformedToken {
		t.Errorf("expected ErrInvalidToken or ErrMalformedToken, got %v", err)
	}
}

func TestChallengerWrongSecret(t *testing.T) {
	c1 := NewChallenger([]byte("secret-1"), 8, time.Minute)
	c2 := NewChallenger([]byte("secret-2"), 8, time.Minute)

	token, err := c1.NewToken()
	if err != nil {
		t.Fatalf("NewToken failed: %v", err)
	}

	nonce := Solve(token, 8)

	// Verification with different secret should fail
	if err := c2.Verify(token, nonce); err != ErrInvalidToken {
		t.Errorf("expected ErrInvalidToken, got %v", err)
	}
}

func TestHasLeadingZeros(t *testing.T) {
	tests := []struct {
		hash     []byte
		bits     int
		expected bool
	}{
		{[]byte{0x00, 0x00, 0xFF}, 16, true},
		{[]byte{0x00, 0x00, 0xFF}, 17, false},
		{[]byte{0x00, 0x0F, 0xFF}, 12, true},
		{[]byte{0x00, 0x0F, 0xFF}, 13, false},
		{[]byte{0x00, 0x07, 0xFF}, 13, true},
		{[]byte{0xFF, 0x00, 0x00}, 0, true},
		{[]byte{0xFF, 0x00, 0x00}, 1, false},
		{[]byte{0x7F, 0x00, 0x00}, 1, true},
		{[]byte{0x3F, 0x00, 0x00}, 2, true},
	}

	for _, tc := range tests {
		result := hasLeadingZeros(tc.hash, tc.bits)
		if result != tc.expected {
			t.Errorf("hasLeadingZeros(%x, %d) = %v, want %v",
				tc.hash, tc.bits, result, tc.expected)
		}
	}
}

func TestMalformedTokens(t *testing.T) {
	c := NewChallenger([]byte("test-secret"), 8, time.Minute)

	tests := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"no dot", "nodothere"},
		{"invalid base64 payload", "!!!.validbase64"},
		{"invalid base64 sig", "dmFsaWQ.!!!"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := c.Verify(tc.token, 0)
			if err != ErrMalformedToken && err != ErrInvalidToken {
				t.Errorf("expected ErrMalformedToken or ErrInvalidToken, got %v", err)
			}
		})
	}
}

func BenchmarkSolve(b *testing.B) {
	c := NewChallenger([]byte("bench-secret"), 16, time.Minute)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		token, _ := c.NewToken()
		_ = Solve(token, 16)
	}
}

func BenchmarkVerify(b *testing.B) {
	c := NewChallenger([]byte("bench-secret"), 16, time.Minute)
	token, _ := c.NewToken()
	nonce := Solve(token, 16)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Verify(token, nonce)
	}
}
