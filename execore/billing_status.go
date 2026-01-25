package execore

import (
	"time"

	"exe.dev/exedb"
)

// billingRequiredDate is when billing became required for new users.
// Users created before this date are grandfathered and don't need billing.
var billingRequiredDate = time.Date(2026, 1, 6, 23, 10, 0, 0, time.UTC)

// userIsPaying returns true if the user has an active billing status.
func userIsPaying(status *exedb.GetUserBillingStatusRow) bool {
	return status.BillingStatus != nil && *status.BillingStatus == "active"
}

// userNeedsBilling returns true if the user needs to add billing before creating VMs.
func userNeedsBilling(status *exedb.GetUserBillingStatusRow) bool {
	// Active users don't need billing
	if status.BillingStatus != nil && *status.BillingStatus == "active" {
		return false
	}
	// CANCELED users ALWAYS need billing (check BEFORE exemptions)
	// This prevents canceled users from bypassing billing even if they have
	// legacy status, free tier, or trial exemptions.
	if status.BillingStatus != nil && *status.BillingStatus == "canceled" {
		return true
	}
	// Users created before billing requirement date are grandfathered
	if status.CreatedAt != nil && status.CreatedAt.Before(billingRequiredDate) {
		return false
	}
	// Free exemptions never need billing
	if status.BillingExemption != nil && *status.BillingExemption == "free" {
		return false
	}
	// Trial exemptions with future end date don't need billing yet
	if status.BillingExemption != nil && *status.BillingExemption == "trial" &&
		status.BillingTrialEndsAt != nil && status.BillingTrialEndsAt.After(time.Now()) {
		return false
	}
	return true
}
