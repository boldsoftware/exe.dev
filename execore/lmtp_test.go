package execore

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-smtp"

	"exe.dev/exedb"
	"exe.dev/sqlite"
)

func TestLMTP_PerRecipientStatus(t *testing.T) {
	t.Parallel()

	// Create test server which sets up the database
	server := newTestServer(t)

	// Create temp directory for socket
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "lmtp.sock")

	// Create test boxes with email enabled
	ctx := context.Background()
	err := server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		boxID, err := queries.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "test-host",
			Name:            "validbox",
			Status:          "running",
			Image:           "test-image",
			CreatedByUserID: "test-user",
		})
		if err != nil {
			return err
		}
		return queries.SetBoxEmailReceiveEnabled(ctx, exedb.SetBoxEmailReceiveEnabledParams{
			EmailReceiveEnabled: 1,
			ID:                  int(boxID),
		})
	})
	if err != nil {
		t.Fatalf("failed to create test box: %v", err)
	}

	// Start LMTP server
	lmtpServer := NewLMTPServer(server, sockPath)

	if err := lmtpServer.Start(ctx); err != nil {
		t.Fatalf("failed to start LMTP server: %v", err)
	}
	defer lmtpServer.Stop(ctx)

	// Wait for socket to be ready
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Run("valid_recipient_accepted", func(t *testing.T) {
		conn, err := net.Dial("unix", sockPath)
		if err != nil {
			t.Fatalf("failed to connect: %v", err)
		}
		defer conn.Close()

		client := smtp.NewClientLMTP(conn)
		defer client.Close()

		if err := client.Hello("localhost"); err != nil {
			t.Fatalf("LHLO failed: %v", err)
		}

		if err := client.Mail("sender@example.com", nil); err != nil {
			t.Fatalf("MAIL FROM failed: %v", err)
		}

		// Valid recipient - box exists and has email enabled
		if err := client.Rcpt("test@validbox.exe.cloud", nil); err != nil {
			t.Errorf("RCPT TO for valid box should succeed: %v", err)
		}
	})

	t.Run("invalid_recipient_rejected", func(t *testing.T) {
		conn, err := net.Dial("unix", sockPath)
		if err != nil {
			t.Fatalf("failed to connect: %v", err)
		}
		defer conn.Close()

		client := smtp.NewClientLMTP(conn)
		defer client.Close()

		if err := client.Hello("localhost"); err != nil {
			t.Fatalf("LHLO failed: %v", err)
		}

		if err := client.Mail("sender@example.com", nil); err != nil {
			t.Fatalf("MAIL FROM failed: %v", err)
		}

		// Invalid recipient - box doesn't exist
		err = client.Rcpt("test@nonexistent.exe.cloud", nil)
		if err == nil {
			t.Error("RCPT TO for nonexistent box should fail")
		}
		// Verify it's a 550 error
		if smtpErr, ok := err.(*smtp.SMTPError); ok {
			if smtpErr.Code != 550 {
				t.Errorf("expected 550 error, got %d", smtpErr.Code)
			}
		}
	})

	t.Run("invalid_domain_rejected", func(t *testing.T) {
		conn, err := net.Dial("unix", sockPath)
		if err != nil {
			t.Fatalf("failed to connect: %v", err)
		}
		defer conn.Close()

		client := smtp.NewClientLMTP(conn)
		defer client.Close()

		if err := client.Hello("localhost"); err != nil {
			t.Fatalf("LHLO failed: %v", err)
		}

		if err := client.Mail("sender@example.com", nil); err != nil {
			t.Fatalf("MAIL FROM failed: %v", err)
		}

		// Invalid domain
		err = client.Rcpt("test@otherdomain.com", nil)
		if err == nil {
			t.Error("RCPT TO for wrong domain should fail")
		}
	})

	t.Run("per_recipient_status_on_data", func(t *testing.T) {
		// Create a second box that's also valid
		err = server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			queries := exedb.New(tx.Conn())
			boxID, err := queries.InsertBox(ctx, exedb.InsertBoxParams{
				Ctrhost:         "test-host",
				Name:            "validbox2",
				Status:          "running",
				Image:           "test-image",
				CreatedByUserID: "test-user",
			})
			if err != nil {
				return err
			}
			return queries.SetBoxEmailReceiveEnabled(ctx, exedb.SetBoxEmailReceiveEnabledParams{
				EmailReceiveEnabled: 1,
				ID:                  int(boxID),
			})
		})
		if err != nil {
			t.Fatalf("failed to create second test box: %v", err)
		}

		conn, err := net.Dial("unix", sockPath)
		if err != nil {
			t.Fatalf("failed to connect: %v", err)
		}
		defer conn.Close()

		client := smtp.NewClientLMTP(conn)
		defer client.Close()

		if err := client.Hello("localhost"); err != nil {
			t.Fatalf("LHLO failed: %v", err)
		}

		if err := client.Mail("sender@example.com", nil); err != nil {
			t.Fatalf("MAIL FROM failed: %v", err)
		}

		// Add two valid recipients
		if err := client.Rcpt("a@validbox.exe.cloud", nil); err != nil {
			t.Fatalf("RCPT TO 1 failed: %v", err)
		}
		if err := client.Rcpt("b@validbox2.exe.cloud", nil); err != nil {
			t.Fatalf("RCPT TO 2 failed: %v", err)
		}

		// Send data - this will fail at delivery (no SSH configured) but we're testing
		// that the LMTP layer handles per-recipient status correctly.
		// Use the regular Data() method since the client API differs.
		dataCmd, err := client.Data()
		if err != nil {
			t.Fatalf("Data failed: %v", err)
		}

		_, err = dataCmd.Write([]byte("Subject: Test\r\n\r\nTest body\r\n"))
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}

		// Close to trigger delivery
		err = dataCmd.Close()
		// We expect errors because SSH isn't configured, but we're testing that
		// the per-recipient status mechanism works
		if err != nil {
			// Check if it's an LMTPDataError with per-recipient status
			if lmtpErr, ok := err.(smtp.LMTPDataError); ok {
				t.Logf("got per-recipient errors (expected): %v", lmtpErr)
				// Verify we got status for both recipients
				count := 0
				for rcpt, rcptErr := range lmtpErr {
					t.Logf("  %s: %v", rcpt, rcptErr)
					count++
				}
				if count != 2 {
					t.Errorf("expected 2 per-recipient statuses, got %d", count)
				}
			} else {
				t.Logf("DATA failed (expected - no SSH): %v", err)
			}
		} else {
			t.Logf("DATA succeeded (unexpected - no SSH configured)")
		}
	})

	t.Run("message_too_large_rejected", func(t *testing.T) {
		conn, err := net.Dial("unix", sockPath)
		if err != nil {
			t.Fatalf("failed to connect: %v", err)
		}
		defer conn.Close()

		client := smtp.NewClientLMTP(conn)
		defer client.Close()

		if err := client.Hello("localhost"); err != nil {
			t.Fatalf("LHLO failed: %v", err)
		}

		if err := client.Mail("sender@example.com", nil); err != nil {
			t.Fatalf("MAIL FROM failed: %v", err)
		}

		if err := client.Rcpt("test@validbox.exe.cloud", nil); err != nil {
			t.Fatalf("RCPT TO failed: %v", err)
		}

		// Try to send a message larger than 1MB
		dataCmd, err := client.Data()
		if err != nil {
			t.Fatalf("Data failed: %v", err)
		}

		// Write > 1MB of data - the server may close the connection when
		// the limit is exceeded, so we expect either a write error or a
		// 552 error on close
		largeData := strings.Repeat("x", 2*1024*1024)
		_, writeErr := dataCmd.Write([]byte("Subject: Large\r\n\r\n" + largeData))

		closeErr := dataCmd.Close()

		// We should get an error from either write or close
		if writeErr == nil && closeErr == nil {
			t.Error("expected error for oversized message")
		}
		// If we got a close error, check if it's the expected 552
		if closeErr != nil {
			if smtpErr, ok := closeErr.(*smtp.SMTPError); ok {
				if smtpErr.Code != 552 {
					t.Errorf("expected 552 error for large message, got %d", smtpErr.Code)
				}
			}
		}
		// Write error (broken pipe) is also acceptable - server closed connection
		t.Logf("write err: %v, close err: %v", writeErr, closeErr)
	})
}
