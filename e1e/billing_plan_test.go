package e1e

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"
)

// TestBillingPlanIndividual tests that a user with Individual billing
// sees the correct plan info from `billing plan`.
func TestBillingPlanIndividual(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDevWithEmail(t, "billing-ind@test-billing-plan.example")
	pty.Disconnect()

	repl := sshToExeDev(t, keyFile)
	repl.SendLine("billing plan")
	repl.Want("Individual Plan (Small)")
	repl.Want("2 vCPUs")
	repl.Want("8 GB memory")
	repl.Want("50 VMs")
	repl.Want("25 GB disk")
	repl.Want("100 GB transfer")
	repl.Want("$20/month")
	repl.Want("/user")
	repl.WantPrompt()
	repl.Disconnect()
}

// TestBillingPlanIndividualJSON tests JSON output of `billing plan --json`.
func TestBillingPlanIndividualJSON(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDevWithEmail(t, "billing-json@test-billing-plan.example")
	pty.Disconnect()

	result := runParseExeDevJSON[map[string]any](t, keyFile, "billing", "plan", "--json")

	if result["plan"] != "Individual" {
		t.Errorf("expected plan=Individual, got %v", result["plan"])
	}
	if result["tier"] != "Small" {
		t.Errorf("expected tier=Small, got %v", result["tier"])
	}
	if result["paid"] != true {
		t.Errorf("expected paid=true, got %v", result["paid"])
	}
	// JSON numbers are float64
	if v, ok := result["max_cpus"].(float64); !ok || v != 2 {
		t.Errorf("expected max_cpus=2, got %v", result["max_cpus"])
	}
	if v, ok := result["max_memory_gb"].(float64); !ok || v != 8 {
		t.Errorf("expected max_memory_gb=8, got %v", result["max_memory_gb"])
	}
	if v, ok := result["monthly_price_cents"].(float64); !ok || v != 2000 {
		t.Errorf("expected monthly_price_cents=2000, got %v", result["monthly_price_cents"])
	}
}

// TestBillingPlanBasic tests that a user without paid billing sees
// the Basic plan info.
func TestBillingPlanBasic(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDevWithoutBilling(t)
	pty.Disconnect()

	repl := sshToExeDev(t, keyFile)
	repl.SendLine("billing plan")
	repl.Want("Plan")
	repl.Want("/user")
	repl.WantPrompt()
	repl.Disconnect()

	// Verify JSON: should not be paid
	result := runParseExeDevJSON[map[string]any](t, keyFile, "billing", "plan", "--json")
	if result["paid"] != false {
		t.Errorf("expected paid=false for basic user, got %v", result["paid"])
	}
}

// TestBillingPlanTeamBillingOwner tests that team billing owners can see
// the billing command and get plan info.
func TestBillingPlanTeamBillingOwner(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	ownerPTY, _, ownerKeyFile, ownerEmail := registerForExeDevWithEmail(t, "owner@test-billing-team.example")
	ownerPTY.Disconnect()

	enableRootSupport(t, ownerEmail)
	createTeam(t, ownerKeyFile, "billing_plan_team", "BillingPlanTeam", ownerEmail)

	repl := sshToExeDev(t, ownerKeyFile)
	repl.SendLine("billing plan")
	repl.Want("Plan") // should show some plan
	repl.Want("/user")
	repl.WantPrompt()
	repl.Disconnect()
}

// TestBillingPlanTeamMemberHidden tests that regular team members cannot
// see the billing command.
func TestBillingPlanTeamMemberHidden(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	ownerPTY, _, ownerKeyFile, ownerEmail := registerForExeDevWithEmail(t, "owner@test-billing-hidden.example")
	memberPTY, _, memberKeyFile, memberEmail := registerForExeDevWithEmail(t, "member@test-billing-hidden.example")
	ownerPTY.Disconnect()
	memberPTY.Disconnect()

	enableRootSupport(t, ownerEmail)
	createTeam(t, ownerKeyFile, "billing_hidden", "BillingHiddenTeam", ownerEmail)
	addTeamMember(t, "billing_hidden", memberEmail)

	// Member should not see billing command
	repl := sshToExeDev(t, memberKeyFile)
	repl.SendLine("billing plan")
	repl.Want("command not available")
	repl.WantPrompt()
	repl.Disconnect()
}

// TestBillingPlanBareCommand tests that bare `billing` shows subcommands.
func TestBillingPlanBareCommand(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDevWithEmail(t, "billing-bare@test-billing-plan.example")
	pty.Disconnect()

	repl := sshToExeDev(t, keyFile)
	repl.SendLine("billing")
	repl.Want("Subcommands")
	repl.Want("plan")
	repl.WantPrompt()
	repl.Disconnect()
}

// TestBillingPlanTrialUser tests that a trial user sees the Trial plan
// without weird output (no price, no tier name like "Standard").
func TestBillingPlanTrialUser(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, email := registerForExeDevWithoutBilling(t)
	pty.Disconnect()

	// Grant trial via debug endpoint
	userID := getUserIDByEmail(t, email)
	grantTrialURL := fmt.Sprintf("http://localhost:%d/debug/users/grant-trial", Env.servers.Exed.HTTPPort)
	resp, err := http.PostForm(grantTrialURL, url.Values{"user_id": {userID}})
	if err != nil {
		t.Fatalf("grant trial failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("grant trial returned %d", resp.StatusCode)
	}

	repl := sshToExeDev(t, keyFile)
	repl.SendLine("billing plan")
	repl.Want("Trial Plan")
	repl.Want("/user")
	repl.WantPrompt()
	repl.Disconnect()

	// Also verify JSON doesn't include price or weird tier info
	result := runParseExeDevJSON[map[string]any](t, keyFile, "billing", "plan", "--json")
	if result["plan"] != "Free" {
		t.Errorf("expected plan=Free, got %v", result["plan"])
	}
	if result["paid"] != false {
		t.Errorf("expected paid=false, got %v", result["paid"])
	}
}

// TestBillingUpdate tests that `billing update` generates a magic link
// that authenticates and redirects to /billing/update.
func TestBillingUpdate(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDevWithEmail(t, "billing-update@test-billing.example")
	pty.Disconnect()

	// Test human-readable output
	repl := sshToExeDev(t, keyFile)
	repl.SendLine("billing update")
	repl.Want("manage your subscription")
	repl.Want("/auth/verify?token=")
	repl.Want("Expires in 15 minutes")
	repl.WantPrompt()
	repl.Disconnect()

	// Test JSON output
	result := runParseExeDevJSON[map[string]string](t, keyFile, "billing", "update", "--json")
	rawURL, ok := result["url"]
	if !ok || rawURL == "" {
		t.Fatalf("expected url in billing update JSON output, got %v", result)
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("failed to parse url %q: %v", rawURL, err)
	}
	if parsed.Path != "/auth/verify" {
		t.Errorf("expected path /auth/verify, got %q", parsed.Path)
	}
	if parsed.Query().Get("token") == "" {
		t.Fatalf("expected token query parameter in url %q", rawURL)
	}

	// Verify the magic link works: sets auth cookie and redirects to /billing/update
	client := noRedirectClient(nil)
	resp, err := client.Get(rawURL)
	if err != nil {
		t.Fatalf("failed to fetch url %q: %v", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusTemporaryRedirect {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected redirect from magic link, got %d\n%s", resp.StatusCode, body)
	}

	// Verify auth cookie is set
	foundAuthCookie := false
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "exe-auth" {
			foundAuthCookie = true
			break
		}
	}
	if !foundAuthCookie {
		t.Errorf("expected exe-auth cookie from magic link response")
	}

	// Verify redirect goes to /billing/update
	location := resp.Header.Get("Location")
	if location == "" {
		t.Fatalf("expected Location header in redirect response")
	}
	redirectURL, err := url.Parse(location)
	if err != nil {
		t.Fatalf("failed to parse redirect Location %q: %v", location, err)
	}
	if redirectURL.Path != "/billing/update" {
		t.Errorf("expected redirect to /billing/update, got %q", redirectURL.Path)
	}
}

// TestBillingUpdateTeamMemberHidden tests that regular team members
// cannot use `billing update`.
func TestBillingUpdateTeamMemberHidden(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	ownerPTY, _, ownerKeyFile, ownerEmail := registerForExeDevWithEmail(t, "owner@test-billing-update-hidden.example")
	memberPTY, _, memberKeyFile, memberEmail := registerForExeDevWithEmail(t, "member@test-billing-update-hidden.example")
	ownerPTY.Disconnect()
	memberPTY.Disconnect()

	enableRootSupport(t, ownerEmail)
	createTeam(t, ownerKeyFile, "billing_upd_hidden", "BillingUpdHiddenTeam", ownerEmail)
	addTeamMember(t, "billing_upd_hidden", memberEmail)

	repl := sshToExeDev(t, memberKeyFile)
	repl.SendLine("billing update")
	repl.Want("command not available")
	repl.WantPrompt()
	repl.Disconnect()
}

// TestBillingBareCommandShowsUpdate tests that bare `billing` lists
// the update subcommand.
func TestBillingBareCommandShowsUpdate(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDevWithEmail(t, "billing-bare-upd@test-billing.example")
	pty.Disconnect()

	repl := sshToExeDev(t, keyFile)
	repl.SendLine("billing")
	repl.Want("Subcommands")
	repl.Want("plan")
	repl.Want("update")
	repl.WantPrompt()
	repl.Disconnect()
}

// TestBillingInvoicesEmpty tests that `billing invoices` shows no invoices
// for a user with no Stripe history.
func TestBillingInvoicesEmpty(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDevWithEmail(t, "invoices-empty@test-billing.example")
	pty.Disconnect()

	repl := sshToExeDev(t, keyFile)
	repl.SendLine("billing invoices")
	repl.Want("No invoices found")
	repl.WantPrompt()
	repl.Disconnect()
}

// TestBillingInvoicesJSON tests that `billing invoices --json` returns
// a valid JSON structure even with no invoices.
func TestBillingInvoicesJSON(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDevWithEmail(t, "invoices-json@test-billing.example")
	pty.Disconnect()

	result := runParseExeDevJSON[map[string]any](t, keyFile, "billing", "invoices", "--json")
	invoices, ok := result["invoices"].([]any)
	if !ok {
		t.Fatalf("expected invoices array, got %T: %v", result["invoices"], result)
	}
	if len(invoices) != 0 {
		t.Errorf("expected empty invoices, got %d", len(invoices))
	}
}

// TestBillingReceiptsEmpty tests that `billing receipts` shows no receipts
// for a user with no credit purchases.
func TestBillingReceiptsEmpty(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDevWithEmail(t, "receipts-empty@test-billing.example")
	pty.Disconnect()

	repl := sshToExeDev(t, keyFile)
	repl.SendLine("billing receipts")
	repl.Want("No credit purchase receipts found")
	repl.WantPrompt()
	repl.Disconnect()
}

// TestBillingReceiptsJSON tests that `billing receipts --json` returns
// a valid JSON structure even with no receipts.
func TestBillingReceiptsJSON(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDevWithEmail(t, "receipts-json@test-billing.example")
	pty.Disconnect()

	result := runParseExeDevJSON[map[string]any](t, keyFile, "billing", "receipts", "--json")
	receipts, ok := result["receipts"].([]any)
	if !ok {
		t.Fatalf("expected receipts array, got %T: %v", result["receipts"], result)
	}
	if len(receipts) != 0 {
		t.Errorf("expected empty receipts, got %d", len(receipts))
	}
}

// TestBillingInvoicesTeamMemberHidden tests that regular team members
// cannot use `billing invoices`.
func TestBillingInvoicesTeamMemberHidden(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	ownerPTY, _, ownerKeyFile, ownerEmail := registerForExeDevWithEmail(t, "owner@test-billing-inv-hidden.example")
	memberPTY, _, memberKeyFile, memberEmail := registerForExeDevWithEmail(t, "member@test-billing-inv-hidden.example")
	ownerPTY.Disconnect()
	memberPTY.Disconnect()

	enableRootSupport(t, ownerEmail)
	createTeam(t, ownerKeyFile, "billing_inv_hidden", "BillingInvHiddenTeam", ownerEmail)
	addTeamMember(t, "billing_inv_hidden", memberEmail)

	repl := sshToExeDev(t, memberKeyFile)
	repl.SendLine("billing invoices")
	repl.Want("command not available")
	repl.WantPrompt()
	repl.Disconnect()
}
