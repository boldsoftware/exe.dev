package e1e

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"exe.dev/e1e/testinfra"
)

// TestDeviceVerificationDoubleClick tests that double-clicking the device
// verification link doesn't cause a 500 error.
//
// Previously, the code used InsertSSHKeyForEmailUser which has no ON CONFLICT
// clause, causing "UNIQUE constraint failed: ssh_keys.public_key" when two
// concurrent requests tried to insert the same key.
func TestDeviceVerificationDoubleClick(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Step 1: Register a user with an initial SSH key
	email := t.Name() + testinfra.FakeEmailSuffix
	keyFile1, _ := genSSHKey(t)
	pty := sshToExeDev(t, keyFile1)
	pty.Want(testinfra.Banner)
	pty.Want("Please enter your email")
	pty.SendLine(email)
	pty.WantRE("Verification email sent to.*" + regexp.QuoteMeta(email))
	waitForEmailAndVerify(t, email)
	pty.Want("Email verified successfully")
	pty.Want("Registration complete")
	pty.WantPrompt()
	pty.Disconnect()

	// Step 2: Generate a NEW SSH key and SSH in with it (triggers device verification)
	keyFile2, _ := genSSHKey(t)
	pty2 := sshToExeDev(t, keyFile2)
	pty2.Want(testinfra.Banner)
	pty2.Want("Please enter your email")
	pty2.SendLine(email)
	pty2.WantRE("Verification email sent to.*" + regexp.QuoteMeta(email))

	// Step 3: Get the verification email and extract the link
	msg, err := Env.servers.Email.WaitForEmail(email)
	if err != nil {
		t.Fatalf("Failed to get verification email: %v", err)
	}

	verifyURL, err := testinfra.ExtractVerificationToken(msg.Body)
	if err != nil {
		t.Fatalf("Failed to extract verification URL: %v", err)
	}

	// Step 4: GET the verification page to get the form data
	getResp, err := http.Get(verifyURL)
	if err != nil {
		t.Fatalf("Failed to GET verification page: %v", err)
	}
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("Verification page returned status %d", getResp.StatusCode)
	}
	htmlBody, err := io.ReadAll(getResp.Body)
	getResp.Body.Close()
	if err != nil {
		t.Fatalf("Failed to read verification page: %v", err)
	}

	// Extract hidden inputs from the form
	formData := testinfra.ExtractFormFields(htmlBody)

	token := formData.Get("token")
	if token == "" {
		t.Fatalf("Failed to extract token from form")
	}

	// Determine form action
	actionPath := testinfra.ExtractFormAction(htmlBody, "/verify-device")

	postURL := fmt.Sprintf("http://localhost:%d%s", Env.servers.Exed.HTTPPort, actionPath)

	// Step 5: Make multiple concurrent POST requests (simulating double-click)
	const nRequests = 2
	var wg sync.WaitGroup
	type result struct {
		status int
		err    error
	}
	results := make(chan result, nRequests)

	for range nRequests {
		wg.Go(func() {
			resp, err := http.PostForm(postURL, formData)
			if err != nil {
				results <- result{err: err}
				return
			}
			resp.Body.Close()
			results <- result{status: resp.StatusCode}
		})
	}

	wg.Wait()
	close(results)

	// Step 6: Check results - both should succeed (no 500 errors)
	var statuses []int
	for r := range results {
		if r.err != nil {
			t.Fatalf("HTTP request failed: %v", r.err)
		}
		statuses = append(statuses, r.status)
	}

	t.Logf("Double-click results: %v", statuses)

	for _, status := range statuses {
		if status == http.StatusInternalServerError {
			t.Errorf("Got 500 Internal Server Error - this indicates the UNIQUE constraint race condition bug")
		}
	}

	// At least one should succeed with 200
	if !slices.Contains(statuses, http.StatusOK) {
		t.Errorf("Expected at least one 200 OK, got: %v", statuses)
	}

	// Step 7: The SSH session should have been notified of verification
	pty2.Want("Email verified successfully")
	pty2.Want("Registration complete")
	pty2.WantPrompt()
	pty2.Disconnect()
}

// TestDeviceVerificationKeyTheft tests that two different users cannot both
// register the same SSH key via a race condition.
//
// This tests the security check that verifies key ownership when the insert
// is a no-op (key already exists). Without this check, an attacker could
// potentially claim another user's pending key by racing the verification.
func TestDeviceVerificationKeyTheft(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Step 1: Register User A (alice) with her own key
	aliceEmail := t.Name() + "-alice" + testinfra.FakeEmailSuffix
	aliceKey, _ := genSSHKey(t)
	pty := sshToExeDev(t, aliceKey)
	pty.Want(testinfra.Banner)
	pty.Want("Please enter your email")
	pty.SendLine(aliceEmail)
	pty.WantRE("Verification email sent to.*" + regexp.QuoteMeta(aliceEmail))
	waitForEmailAndVerify(t, aliceEmail)
	pty.Want("Email verified successfully")
	pty.Want("Registration complete")
	pty.WantPrompt()
	pty.Disconnect()

	// Step 2: Register User B (bob) with his own key
	bobEmail := t.Name() + "-bob" + testinfra.FakeEmailSuffix
	bobKey, _ := genSSHKey(t)
	pty = sshToExeDev(t, bobKey)
	pty.Want(testinfra.Banner)
	pty.Want("Please enter your email")
	pty.SendLine(bobEmail)
	pty.WantRE("Verification email sent to.*" + regexp.QuoteMeta(bobEmail))
	waitForEmailAndVerify(t, bobEmail)
	pty.Want("Email verified successfully")
	pty.Want("Registration complete")
	pty.WantPrompt()
	pty.Disconnect()

	// Step 3: Generate a shared key that both users will try to register
	sharedKeyFile, _ := genSSHKey(t)

	// Step 4: Both users SSH with the shared key and start verification
	// Alice's session
	alicePty := sshToExeDev(t, sharedKeyFile)
	alicePty.Want(testinfra.Banner)
	alicePty.Want("Please enter your email")
	alicePty.SendLine(aliceEmail)
	alicePty.WantRE("Verification email sent to.*" + regexp.QuoteMeta(aliceEmail))

	// Bob's session
	bobPty := sshToExeDev(t, sharedKeyFile)
	bobPty.Want(testinfra.Banner)
	bobPty.Want("Please enter your email")
	bobPty.SendLine(bobEmail)
	bobPty.WantRE("Verification email sent to.*" + regexp.QuoteMeta(bobEmail))

	// Step 5: Get both verification emails and extract form data
	getVerificationFormData := func(email string) (postURL string, formData url.Values) {
		msg, err := Env.servers.Email.WaitForEmail(email)
		if err != nil {
			t.Fatalf("Failed to get verification email for %s: %v", email, err)
		}

		verifyURL, err := testinfra.ExtractVerificationToken(msg.Body)
		if err != nil {
			t.Fatalf("Failed to extract verification URL for %s: %v", email, err)
		}

		getResp, err := http.Get(verifyURL)
		if err != nil {
			t.Fatalf("Failed to GET verification page for %s: %v", email, err)
		}
		if getResp.StatusCode != http.StatusOK {
			t.Fatalf("Verification page for %s returned status %d", email, getResp.StatusCode)
		}
		htmlBody, err := io.ReadAll(getResp.Body)
		getResp.Body.Close()
		if err != nil {
			t.Fatalf("Failed to read verification page for %s: %v", email, err)
		}

		formData = testinfra.ExtractFormFields(htmlBody)

		actionPath := testinfra.ExtractFormAction(htmlBody, "/verify-device")

		postURL = fmt.Sprintf("http://localhost:%d%s", Env.servers.Exed.HTTPPort, actionPath)
		return postURL, formData
	}

	alicePostURL, aliceFormData := getVerificationFormData(aliceEmail)
	bobPostURL, bobFormData := getVerificationFormData(bobEmail)

	// Step 6: Race both verifications
	var wg sync.WaitGroup
	var aliceStatus, bobStatus atomic.Int32

	wg.Go(func() {
		resp, err := http.PostForm(alicePostURL, aliceFormData)
		if err != nil {
			t.Errorf("Alice's POST failed: %v", err)
			return
		}
		resp.Body.Close()
		aliceStatus.Store(int32(resp.StatusCode))
	})

	wg.Go(func() {
		resp, err := http.PostForm(bobPostURL, bobFormData)
		if err != nil {
			t.Errorf("Bob's POST failed: %v", err)
			return
		}
		resp.Body.Close()
		bobStatus.Store(int32(resp.StatusCode))
	})

	wg.Wait()

	aliceCode := int(aliceStatus.Load())
	bobCode := int(bobStatus.Load())
	t.Logf("Alice status: %d, Bob status: %d", aliceCode, bobCode)

	// Step 7: Verify security behavior
	// Exactly one should succeed (200), exactly one should fail (500)
	// The 500 is expected because the security check rejects the second user
	successCount := 0
	failCount := 0
	for _, code := range []int{aliceCode, bobCode} {
		switch code {
		case http.StatusOK:
			successCount++
		case http.StatusInternalServerError:
			failCount++
		case 0:
			t.Errorf("HTTP request failed (status 0)")
		default:
			t.Errorf("Unexpected status code: %d", code)
		}
	}

	if successCount != 1 {
		t.Errorf("Expected exactly 1 success, got %d", successCount)
	}
	if failCount != 1 {
		t.Errorf("Expected exactly 1 failure (security rejection), got %d", failCount)
	}

	// Step 8: Clean up SSH sessions
	// The winner's session completed verification; the loser is stuck waiting.
	// Handle each based on their verification result.
	if aliceCode == http.StatusOK {
		alicePty.Want("Email verified successfully")
		alicePty.Want("Registration complete")
		alicePty.WantPrompt()
		alicePty.Disconnect()
		bobPty.Close() // Bob is stuck waiting; force close
	} else {
		bobPty.Want("Email verified successfully")
		bobPty.Want("Registration complete")
		bobPty.WantPrompt()
		bobPty.Disconnect()
		alicePty.Close() // Alice is stuck waiting; force close
	}
}
