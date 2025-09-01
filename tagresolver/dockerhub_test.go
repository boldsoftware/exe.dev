package tagresolver

import (
	"context"
	"strings"
	"testing"
)

func TestDockerHubUbuntuResolution(t *testing.T) {
	// This test requires network access
	if testing.Short() {
		t.Skip("Skipping Docker Hub test in short mode")
	}

	db, cleanup := setupTestDB(t)
	defer cleanup()

	tr := New(db)
	ctx := context.Background()

	testCases := []struct {
		image    string
		platform string
		name     string
	}{
		{
			name:     "ubuntu:latest with platform",
			image:    "ubuntu:latest",
			platform: "linux/amd64",
		},
		{
			name:     "ubuntu:22.04 with platform",
			image:    "ubuntu:22.04",
			platform: "linux/amd64",
		},
		{
			name:     "library/ubuntu:latest",
			image:    "library/ubuntu:latest",
			platform: "linux/amd64",
		},
		{
			name:     "docker.io/library/ubuntu:22.04",
			image:    "docker.io/library/ubuntu:22.04",
			platform: "linux/amd64",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tr.ResolveTag(ctx, tc.image, tc.platform)
			if err != nil {
				t.Fatalf("Failed to resolve %s: %v", tc.image, err)
			}

			t.Logf("Resolved %s to %s", tc.image, result)

			// Should return a digest reference
			if !strings.Contains(result, "@sha256:") {
				t.Errorf("Expected digest reference, got: %s", result)
			}

			// The digest should NOT be any of these known manifest list digests
			knownManifestListDigests := []string{
				"sha256:62767d3a9e5db738ab69e115188c8e61b80ff4eb7ba70c083b2e766fe15e92e4", // Wrong digest we're seeing
				"sha256:1aa979d85661c488ce030ac292876cf6ed04535d3a237e49f61542d8e5de5ae0", // ubuntu:22.04 OCI index
				"sha256:7c06e91f61fa88c08cc74f7e1b7c69ae24910d745357e0dfe1d2c0322aaf20f9", // ubuntu:latest OCI index
			}

			for _, badDigest := range knownManifestListDigests {
				if strings.Contains(result, badDigest) {
					t.Errorf("Returned manifest list digest %s instead of platform-specific digest", badDigest)
				}
			}

			// Known good platform-specific digests (these may change over time)
			// These are examples from our test output
			knownGoodDigests := []string{
				"sha256:1f809e07a0402375e7b2ede95e4c43f5605a83c447d1a4ca9c9d3c4194440104", // ubuntu:22.04 linux/amd64
				"sha256:35f3a8badf2f74c1b320a643b343536f5132f245cbefc40ef802b6203a166d04", // ubuntu:latest linux/amd64
			}

			// Check if we got one of the known good digests (or a newer one)
			foundGoodDigest := false
			for _, goodDigest := range knownGoodDigests {
				if strings.Contains(result, goodDigest) {
					foundGoodDigest = true
					t.Logf("Got expected platform-specific digest: %s", goodDigest)
					break
				}
			}

			if !foundGoodDigest {
				// It might be a newer digest, which is fine as long as it's not a manifest list
				t.Logf("Got unknown digest (might be newer): %s", result)
			}
		})
	}
}

func TestGetManifestDigestDirectly(t *testing.T) {
	// This test requires network access
	if testing.Short() {
		t.Skip("Skipping direct manifest test in short mode")
	}

	db, cleanup := setupTestDB(t)
	defer cleanup()

	tr := New(db)
	ctx := context.Background()

	// Test getManifestDigest directly
	digest, size, err := tr.getManifestDigest(ctx, "docker.io", "library/ubuntu", "22.04", "linux/amd64")
	if err != nil {
		t.Fatalf("Failed to get manifest digest: %v", err)
	}

	t.Logf("Direct getManifestDigest returned: digest=%s, size=%d", digest, size)

	// Should NOT return the OCI index digest
	if digest == "sha256:1aa979d85661c488ce030ac292876cf6ed04535d3a237e49f61542d8e5de5ae0" {
		t.Error("Returned OCI index digest instead of platform-specific digest")
	}

	// Should NOT return the wrong digest we're seeing
	if digest == "sha256:62767d3a9e5db738ab69e115188c8e61b80ff4eb7ba70c083b2e766fe15e92e4" {
		t.Error("Returned the wrong digest sha256:62767d3a...")
	}
}
