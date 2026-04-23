package e1e

import (
	"fmt"
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
// without weird output (no price, no tier name like "Default").
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
	if result["plan"] != "Trial" {
		t.Errorf("expected plan=Trial, got %v", result["plan"])
	}
	if result["paid"] != false {
		t.Errorf("expected paid=false, got %v", result["paid"])
	}
}
