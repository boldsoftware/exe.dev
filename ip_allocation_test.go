package exe

import (
	"context"
	"fmt"
	"testing"

	"exe.dev/sqlite"
)

func TestIPRangeAllocation(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	ctx := t.Context()

	t.Run("FirstAllocationGetsFirstRange", func(t *testing.T) {
		// Create a user with alloc
		userID := "test-user-1"
		email := "user1@example.com"
		publicKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFrstAllocationTest test@example.com"

		err := server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, email); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key) VALUES (?, ?)`, userID, publicKey); err != nil {
				return err
			}

			// Allocate IP range
			dockerHost := "test-host"
			ipRange, err := server.allocateIPRange(ctx, tx, dockerHost)
			if err != nil {
				return err
			}

			// Should get the first available range
			if ipRange != "10.42.0.0/24" {
				t.Errorf("Expected first allocation to get 10.42.0.0/24, got %s", ipRange)
			}

			// Create alloc with the IP range
			_, err = tx.Exec(`
				INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, ip_range, billing_email)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				"alloc-1", userID, AllocTypeMedium, RegionAWSUSWest2, dockerHost, ipRange, email)
			return err
		})
		if err != nil {
			t.Fatalf("Failed to create first alloc: %v", err)
		}
	})

	t.Run("SecondAllocationGetsDifferentRange", func(t *testing.T) {
		// Create a second user with alloc
		userID := "test-user-2"
		email := "user2@example.com"
		publicKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAISecondAllocationTest test2@example.com"

		err := server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, email); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key) VALUES (?, ?)`, userID, publicKey); err != nil {
				return err
			}

			// Allocate IP range
			dockerHost := "test-host"
			ipRange, err := server.allocateIPRange(ctx, tx, dockerHost)
			if err != nil {
				return err
			}

			// Should get the second available range
			if ipRange != "10.42.1.0/24" {
				t.Errorf("Expected second allocation to get 10.42.1.0/24, got %s", ipRange)
			}

			// Create alloc with the IP range
			_, err = tx.Exec(`
				INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, ip_range, billing_email)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				"alloc-2", userID, AllocTypeMedium, RegionAWSUSWest2, dockerHost, ipRange, email)
			return err
		})
		if err != nil {
			t.Fatalf("Failed to create second alloc: %v", err)
		}
	})

	t.Run("DifferentHostsCanUseSameRange", func(t *testing.T) {
		// Create a third user with alloc on a different host
		userID := "test-user-3"
		email := "user3@example.com"
		publicKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIThirdAllocationTest test3@example.com"

		err := server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, email); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key) VALUES (?, ?)`, userID, publicKey); err != nil {
				return err
			}

			// Allocate IP range for a different docker host
			dockerHost := "different-host"
			ipRange, err := server.allocateIPRange(ctx, tx, dockerHost)
			if err != nil {
				return err
			}

			// Should get the first range again since it's a different host
			if ipRange != "10.42.0.0/24" {
				t.Errorf("Expected different host to get 10.42.0.0/24, got %s", ipRange)
			}

			// Create alloc with the IP range
			_, err = tx.Exec(`
				INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, ip_range, billing_email)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				"alloc-3", userID, AllocTypeMedium, RegionAWSUSWest2, dockerHost, ipRange, email)
			return err
		})
		if err != nil {
			t.Fatalf("Failed to create third alloc: %v", err)
		}
	})

	t.Run("AllocateMultipleRanges", func(t *testing.T) {
		// Test allocating many ranges to ensure the pattern works
		dockerHost := "bulk-test-host"
		allocatedRanges := make(map[string]bool)

		for i := 0; i < 100; i++ {
			userID := fmt.Sprintf("bulk-user-%d", i)
			email := fmt.Sprintf("bulk%d@example.com", i)
			allocID := fmt.Sprintf("bulk-alloc-%d", i)

			err := server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
				if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, email); err != nil {
					return err
				}

				// Allocate IP range
				ipRange, err := server.allocateIPRange(ctx, tx, dockerHost)
				if err != nil {
					return err
				}

				// Check for duplicates
				if allocatedRanges[ipRange] {
					t.Errorf("Duplicate IP range allocated: %s", ipRange)
				}
				allocatedRanges[ipRange] = true

				// Verify format is 10.X.Y.0/24
				var x, y int
				if n, err := fmt.Sscanf(ipRange, "10.%d.%d.0/24", &x, &y); err != nil || n != 2 {
					t.Errorf("Invalid IP range format: %s", ipRange)
				} else {
					// Check X is in valid range (42-99)
					if x < 42 || x > 99 {
						t.Errorf("IP range X value out of range: %d in %s", x, ipRange)
					}
					// Check Y is in valid range (0-255)
					if y < 0 || y > 255 {
						t.Errorf("IP range Y value out of range: %d in %s", y, ipRange)
					}
				}

				// Create alloc with the IP range
				_, err = tx.Exec(`
					INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, ip_range, billing_email)
					VALUES (?, ?, ?, ?, ?, ?, ?)`,
					allocID, userID, AllocTypeMedium, RegionAWSUSWest2, dockerHost, ipRange, email)
				return err
			})
			if err != nil {
				t.Fatalf("Failed to create alloc %d: %v", i, err)
			}
		}

		// Verify we allocated 100 unique ranges
		if len(allocatedRanges) != 100 {
			t.Errorf("Expected 100 unique ranges, got %d", len(allocatedRanges))
		}
	})

	t.Run("BackwardCompatibility", func(t *testing.T) {
		// Test that existing allocs without IP ranges still work
		userID := "legacy-user"
		email := "legacy@example.com"
		allocID := "legacy-alloc"

		err := server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
			if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, email); err != nil {
				return err
			}
			// Create alloc without IP range (simulating existing data)
			_, err := tx.Exec(`
				INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, billing_email)
				VALUES (?, ?, ?, ?, ?, ?)`,
				allocID, userID, AllocTypeMedium, RegionAWSUSWest2, "legacy-host", email)
			return err
		})
		if err != nil {
			t.Fatalf("Failed to create legacy alloc: %v", err)
		}

		// Get the alloc - should work even without IP range
		alloc, err := server.getUserAlloc(ctx, userID)
		if err != nil {
			t.Fatalf("Failed to get legacy alloc: %v", err)
		}

		if alloc.AllocID != allocID {
			t.Errorf("Expected alloc ID %s, got %s", allocID, alloc.AllocID)
		}

		// IP range should be NULL for legacy alloc
		if alloc.IPRange.Valid {
			t.Errorf("Expected NULL IP range for legacy alloc, got %s", alloc.IPRange.String)
		}
	})
}
