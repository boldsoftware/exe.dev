package e1e

import (
	"encoding/json"
	"testing"

	"exe.dev/vouch"
)

func TestNewUserCredits(t *testing.T) {
	vouch.For("philip")
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	defer pty.disconnect()

	pty.sendLine("billing")
	pty.want("Billing Information:")
	pty.want("Current Balance:")
	pty.want("100.00")
	pty.wantPrompt()

	// Also test via JSON API
	billingOut, err := runExeDevSSHCommand(t, keyFile, "billing", "--json")
	if err != nil {
		t.Fatalf("failed to run billing command: %v\n%s", err, billingOut)
	}

	var billing billingOutput
	err = json.Unmarshal(billingOut, &billing)
	if err != nil {
		t.Fatalf("failed to parse billing output as JSON: %v\n%s", err, billingOut)
	}

	if billing.CurrentBalanceUSD != 100.0 {
		t.Errorf("expected new user to have $100.00 in credits, got $%.2f", billing.CurrentBalanceUSD)
	}
}

type billingOutput struct {
	Configured        bool    `json:"configured"`
	Email             string  `json:"email"`
	StripeCustomerID  string  `json:"stripe_customer_id"`
	CurrentBalanceUSD float64 `json:"current_balance_usd"`
}
