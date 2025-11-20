package users

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	_ "embed"

	"exe.dev/sqlite"
	"golang.org/x/crypto/ssh"
)

func newDB(t *testing.T) *sqlite.DB {
	t.Helper()

	path := filepath.Join(t.TempDir(), "exe.db")

	db, err := sqlite.New(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(t.Context(), Schema); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		db.Close()

		if t.Failed() {
			dstPath := filepath.Join("testdbs", fmt.Sprintf("%s_%s.db", t.Name(), rand.Text()[:8]))
			dstPath, _ = filepath.Abs(dstPath)

			os.MkdirAll(filepath.Dir(dstPath), 0o755)

			out, err := exec.Command("sqlite3", path, ".clone "+dstPath).CombinedOutput()
			if err != nil {
				t.Log("failed to preserve test database:")
				t.Logf("  err:    %v", err)
				t.Logf("  output: %s", out)
				return
			}

			// Log the path to the preserved database.
			//
			// Use a URI so terminals and IDEs can auto-detect it
			// as a database link and provide a convenient "Open in
			// SQLite viewer" option.
			t.Logf("preserved database at sqlite3://%s", dstPath)
		}
	})

	return db
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db := newDB(t)
	return New(db)
}

func TestLoginSSH(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestStore(t)

		alice := generateKey()

		u, err := s.LoginSSH(t.Context(), alice)
		if err != nil {
			t.Fatal(err)
		}
		checkLogin(t, u, &Login{
			UserID:    u.UserID,
			Email:     "",
			PublicKey: alice,
			LastUsed:  time.Now(),
		})

		time.Sleep(1 * time.Hour)

		u2, err := s.LoginSSH(t.Context(), alice)
		if err != nil {
			t.Fatal(err)
		}
		checkLogin(t, u2, &Login{
			UserID:    u.UserID,
			Email:     "",
			PublicKey: alice,
			LastUsed:  time.Now(),
		})
	})
}

func TestEmailVerificationWithoutPublicKey(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestStore(t)

		_, err := s.StartEmailVerification(t.Context(), "invalid-email", nil)
		checkErr(t, err, ErrInvalid)

		code, err := s.StartEmailVerification(t.Context(), "alice@palace.com", nil)
		checkErr(t, err, nil)
		if code == "" {
			t.Fatalf("expected non-empty code")
		}

		_, err = s.CompleteEmailVerification(t.Context(), code)
		checkErr(t, err, nil)

		// complete successfully
		_, err = s.CompleteEmailVerification(t.Context(), code)
		checkErr(t, err, ErrExpired)

		// try to complete again and see "expired"
		_, err = s.CompleteEmailVerification(t.Context(), code)
		checkErr(t, err, ErrExpired)

		code, err = s.StartEmailVerification(t.Context(), "alice@palace.com", nil)
		checkErr(t, err, nil)

		time.Sleep(15 * time.Minute) // let the code expire

		_, err = s.CompleteEmailVerification(t.Context(), code)
		checkErr(t, err, ErrExpired)
	})
}

func TestCompleteEmailVerificationTwoKeys(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestStore(t)

		const email = "alice@palace.com"

		firstSSH, err := s.LoginSSH(t.Context(), generateKey())
		checkErr(t, err, nil)
		firstCode, err := s.StartEmailVerification(t.Context(), email, firstSSH.PublicKey)
		checkErr(t, err, nil)
		firstLogin, err := s.CompleteEmailVerification(t.Context(), firstCode)
		checkErr(t, err, nil)

		secondSSH, err := s.LoginSSH(t.Context(), generateKey())
		checkErr(t, err, nil)
		secondCode, err := s.StartEmailVerification(t.Context(), email, secondSSH.PublicKey)
		checkErr(t, err, nil)
		secondLogin, err := s.CompleteEmailVerification(t.Context(), secondCode)
		checkErr(t, err, nil)

		checkLogin(t, secondLogin, &Login{
			UserID:    firstLogin.UserID,
			Email:     firstLogin.Email,
			PublicKey: secondSSH.PublicKey,
			LastUsed:  secondSSH.LastUsed,
		})

		confirmed, err := s.LoginSSH(t.Context(), secondSSH.PublicKey)
		checkErr(t, err, nil)
		checkLogin(t, confirmed, secondLogin)
	})
}

func TestEmailVerificationWithPublicKey(t *testing.T) {
	t.Run("fails without LoginSSH", func(t *testing.T) {
		s := newTestStore(t)

		alice := generateKey()

		// Skipping LoginSSH before starting email verification with a public key
		// should fail.

		_, err := s.StartEmailVerification(t.Context(), "alice@palace.com", alice)
		if !strings.Contains(err.Error(), "unrecognized public key") {
			t.Fatal("expected error when starting email verification with unrecognized public key")
		}
	})

	t.Run("success", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newTestStore(t)

			alice := generateKey()

			u, err := s.LoginSSH(t.Context(), alice)
			checkErr(t, err, nil)
			checkLogin(t, u, &Login{
				UserID:    u.UserID,
				Email:     "",
				PublicKey: alice,
				LastUsed:  time.Now(),
			})

			code, err := s.StartEmailVerification(t.Context(), "alice@palace.com", alice)
			checkErr(t, err, nil)

			login0, err := s.CompleteEmailVerification(t.Context(), code)
			checkErr(t, err, nil)
			checkLogin(t, login0, &Login{
				UserID:    login0.UserID,
				Email:     "alice@palace.com",
				PublicKey: alice,
				LastUsed:  time.Now(),
			})

			login1, err := s.LoginSSH(t.Context(), alice)
			checkErr(t, err, nil)
			checkLogin(t, login0, login1) // synctest guarantees time.Now() is stable for LastUsed

			_, err = s.CompleteEmailVerification(t.Context(), code)
			checkErr(t, err, ErrExpired)
		})

		t.Run("lapsed", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := newTestStore(t)

				alice := generateKey()

				u, err := s.LoginSSH(t.Context(), alice)
				checkErr(t, err, nil)
				checkLogin(t, u, &Login{
					UserID:    u.UserID,
					Email:     "",
					PublicKey: alice,
					LastUsed:  time.Now(),
				})

				code, err := s.StartEmailVerification(t.Context(), "alice@palace.com", alice)
				checkErr(t, err, nil)

				time.Sleep(15 * time.Minute) // let the code expire

				_, err = s.CompleteEmailVerification(t.Context(), code)
				checkErr(t, err, ErrExpired)
			})
		})
	})
}

func TestMultipleStartEmailVerification(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestStore(t)

		_, err := s.LoginSSH(t.Context(), generateKey())
		checkErr(t, err, nil)

		first, err := s.StartEmailVerification(t.Context(), "alice@palace.com", nil)
		checkErr(t, err, nil)

		second, err := s.StartEmailVerification(t.Context(), "alice@palace.com", nil)
		checkErr(t, err, nil)

		if second == first {
			t.Fatalf("second code matched first: %q", second)
		}

		_, err = s.ReviewEmailVerification(t.Context(), first)
		checkErr(t, err, ErrExpired)

		ev, err := s.ReviewEmailVerification(t.Context(), second)
		checkErr(t, err, nil)
		if ev.Code != second {
			t.Fatalf("code: got %q; want %q", ev.Code, second)
		}
	})
}

func TestStartEmailVerificationWithPublicKey(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestStore(t)

		alice := generateKey()
		_, err := s.LoginSSH(t.Context(), alice)
		checkErr(t, err, nil)

		first, err := s.StartEmailVerification(t.Context(), "alice@palace.com", alice)
		checkErr(t, err, nil)
		second, err := s.StartEmailVerification(t.Context(), "alice@palace.com", alice)
		checkErr(t, err, nil)

		if second == first {
			t.Fatalf("second code matched first: %q", second)
		}

		_, err = s.ReviewEmailVerification(t.Context(), first)
		checkErr(t, err, ErrExpired)

		ev, err := s.ReviewEmailVerification(t.Context(), second)
		checkErr(t, err, nil)
		if ev.Code != second {
			t.Fatalf("code: got %q; want %q", ev.Code, second)
		}
	})
}

func TestStartEmailVerificationRemovesExpired(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestStore(t)

		first, err := s.StartEmailVerification(t.Context(), "alice@palace.com", nil)
		checkErr(t, err, nil)

		stats, err := s.EmailVerificationStats(t.Context())
		checkErr(t, err, nil)
		if stats.Total != 1 {
			t.Fatalf("Total = %d; want 1", stats.Total)
		}

		time.Sleep(15 * time.Minute) // let the code expire

		stats, err = s.EmailVerificationStats(t.Context())
		checkErr(t, err, nil)
		if stats.Total != 1 {
			t.Fatalf("Total = %d; want 0", stats.Total)
		}
		if stats.Expired != 1 {
			t.Fatalf("Expired = %d; want 1", stats.Expired)
		}

		second, err := s.StartEmailVerification(t.Context(), "alice@palace.com", nil)
		checkErr(t, err, nil)

		stats, err = s.EmailVerificationStats(t.Context())
		checkErr(t, err, nil)
		if stats.Total != 1 {
			t.Fatalf("Total = %d; want 1", stats.Total)
		}
		if stats.Expired != 0 {
			t.Fatalf("Expired = %d; want 0", stats.Expired)
		}

		_, err = s.ReviewEmailVerification(t.Context(), first)
		checkErr(t, err, ErrExpired)

		ev, err := s.ReviewEmailVerification(t.Context(), second)
		checkErr(t, err, nil)
		if ev.Code != second {
			t.Fatalf("code: got %q; want %q", ev.Code, second)
		}
	})
}

func TestEmailVerificationStats(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestStore(t)

		stats, err := s.EmailVerificationStats(t.Context())
		checkErr(t, err, nil)
		if stats.Total != 0 {
			t.Fatalf("Total = %d; want 0", stats.Total)
		}

		code, err := s.StartEmailVerification(t.Context(), "alice@palace.com", nil)
		checkErr(t, err, nil)

		stats, err = s.EmailVerificationStats(t.Context())
		checkErr(t, err, nil)
		if stats.Total != 1 {
			t.Fatalf("Total = %d; want 1", stats.Total)
		}

		_, err = s.CompleteEmailVerification(t.Context(), code)
		checkErr(t, err, nil)

		stats, err = s.EmailVerificationStats(t.Context())
		checkErr(t, err, nil)
		if stats.Total != 0 {
			t.Fatalf("Total = %d; want 0", stats.Total)
		}
	})
}

func TestLookupIncompleteVerification(t *testing.T) {
	t.Run("withPublicKey", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newTestStore(t)

			alice := generateKey()
			_, err := s.LoginSSH(t.Context(), alice)
			checkErr(t, err, nil)

			code, err := s.StartEmailVerification(t.Context(), "alice@palace.com", alice)
			checkErr(t, err, nil)

			ev, err := s.ReviewEmailVerification(t.Context(), code)
			checkErr(t, err, nil)
			if ev.Email != "alice@palace.com" {
				t.Fatalf("Email: got %q; want alice@palace.com", ev.Email)
			}
			if ev.PublicKey == nil || !slices.Equal(ev.PublicKey.Marshal(), alice.Marshal()) {
				t.Fatalf("PublicKey mismatch")
			}
			if !ev.CreatedAt.Equal(time.Now()) {
				t.Fatal("CreatedAt not set to now")
			}
			if !ev.ExpiresAt.Equal(time.Now().Add(15 * time.Minute)) {
				t.Fatal("ExpiresAt not set to now")
			}
		})
	})

	t.Run("withoutPublicKey", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newTestStore(t)

			code, err := s.StartEmailVerification(t.Context(), "alice@palace.com", nil)
			checkErr(t, err, nil)

			ev, err := s.ReviewEmailVerification(t.Context(), code)
			checkErr(t, err, nil)
			if ev.PublicKey != nil {
				t.Fatalf("PublicKey: got %v; want nil", ev.PublicKey.Type())
			}
		})
	})

	t.Run("unknownCode", func(t *testing.T) {
		s := newTestStore(t)
		_, err := s.ReviewEmailVerification(t.Context(), "nope")
		checkErr(t, err, ErrExpired)
	})

	t.Run("expired", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s := newTestStore(t)

			code, err := s.StartEmailVerification(t.Context(), "alice@palace.com", nil)
			checkErr(t, err, nil)

			time.Sleep(15 * time.Minute)

			_, err = s.ReviewEmailVerification(t.Context(), code)
			checkErr(t, err, ErrExpired)
		})
	})
}

func checkErr(t *testing.T, got, want error) {
	t.Helper()
	if want == nil && got != nil {
		t.Fatalf("unexpected error: %v", got)
	}
	if !errors.Is(got, want) {
		t.Errorf("err = %v; want %v", got, want)
	}
}

func checkLogin(t *testing.T, got, want *Login) {
	t.Helper()

	errorf := func(format string, args ...any) {
		if !t.Failed() {
			t.Log("Login mismatch:")
		}

		for i := range args {
			switch v := args[i].(type) {
			case ssh.PublicKey:
				args[i] = bytes.TrimSuffix(ssh.MarshalAuthorizedKey(v), []byte("\n"))
			}
		}

		fmt.Fprintf(t.Output(), format+"\n", args...)
		t.Fail()
	}

	if got.UserID != want.UserID {
		errorf("UserID = %s; want %s", got.UserID, want.UserID)
	}
	if got.Email != want.Email {
		errorf("Email = %q; want %q", got.Email, want.Email)
	}
	if got.PublicKey == nil {
		if want.PublicKey != nil {
			errorf("PublicKey = nil; want %q", want.PublicKey)
		}
	} else if want.PublicKey == nil {
		errorf("PublicKey = %q; want nil", got.PublicKey)
	} else if !slices.Equal(got.PublicKey.Marshal(), want.PublicKey.Marshal()) {
		errorf("PublicKey = %q; want %q", got.PublicKey, want.PublicKey)
	}

	d := got.LastUsed.Sub(want.LastUsed)
	if d != 0 {
		errorf("LastUsed = %v; want %v", got.LastUsed, want.LastUsed)
	}
}

func generateKey() ssh.PublicKey {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		panic(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		panic(err)
	}
	return signer.PublicKey()
}
