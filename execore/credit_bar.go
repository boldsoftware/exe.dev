package execore

import (
	"fmt"
	"strings"

	"exe.dev/billing"
)

// creditBarInput holds the inputs needed to compute credit bar display values.
type creditBarInput struct {
	// shelleyCreditsAvailable is the user's current available credit in USD (monthly + bonus).
	shelleyCreditsAvailable float64
	// planMaxCredit is the base monthly plan capacity in USD (e.g. $20).
	planMaxCredit float64
	// bonusRemaining is the portion of available credit attributable to the upgrade bonus.
	bonusRemaining float64
	// bonusGrantAmount is the original one-time bonus grant in USD (e.g. $100).
	bonusGrantAmount float64
	// extraCreditsUSD is the user's purchased extra credit balance in USD.
	extraCreditsUSD float64
	// giftCreditsUSD is the total gift credits amount in USD (from billing ledger + detected support gifts).
	giftCreditsUSD float64
}

// creditBarResult holds the computed credit bar display values.
type creditBarResult struct {
	totalRemainingPct float64
	usedCreditsUSD    float64
	usedBarPct        float64
	totalCapacity     float64
	monthlyAvailable  float64
	bonusRemaining    float64
	giftCreditsUSD    float64
}

// computeCreditBar calculates a single unified bar.
//
// Capacity = planMax + bonusGrant + extra + giftCredits (fixed denominator).
// All credit pools are combined into one bar that depletes as credits are used.
func computeCreditBar(in creditBarInput) creditBarResult {
	monthlyAvailable := in.shelleyCreditsAvailable
	if monthlyAvailable > in.planMaxCredit {
		monthlyAvailable = in.planMaxCredit
	}
	if monthlyAvailable < 0 {
		monthlyAvailable = 0
	}

	bonusRemaining := in.bonusRemaining
	if bonusRemaining < 0 {
		bonusRemaining = 0
	}

	giftCredits := in.giftCreditsUSD
	if giftCredits < 0 {
		giftCredits = 0
	}

	totalCapacity := in.planMaxCredit + in.bonusGrantAmount + in.extraCreditsUSD + giftCredits
	if totalCapacity < 0 {
		totalCapacity = 0
	}

	remaining := monthlyAvailable + bonusRemaining + in.extraCreditsUSD + giftCredits

	var totalRemainingPct float64
	if totalCapacity > 0 {
		totalRemainingPct = (remaining / totalCapacity) * 100
	}
	if totalRemainingPct < 0 {
		totalRemainingPct = 0
	} else if totalRemainingPct > 100 {
		totalRemainingPct = 100
	}

	usedCreditsUSD := totalCapacity - remaining
	if usedCreditsUSD < 0 {
		usedCreditsUSD = 0
	}
	var usedBarPct float64
	if totalCapacity > 0 {
		usedBarPct = (usedCreditsUSD / totalCapacity) * 100
	}
	if usedBarPct > 100 {
		usedBarPct = 100
	}

	return creditBarResult{
		totalRemainingPct: totalRemainingPct,
		usedCreditsUSD:    usedCreditsUSD,
		usedBarPct:        usedBarPct,
		totalCapacity:     totalCapacity,
		monthlyAvailable:  monthlyAvailable,
		bonusRemaining:    bonusRemaining,
		giftCreditsUSD:    giftCredits,
	}
}

// monthlyUsedPct computes the percentage of monthly allowance used.
func monthlyUsedPct(available, max float64) float64 {
	if max <= 0 {
		return 0
	}
	used := max - available
	if used < 0 {
		used = 0
	}
	pct := (used / max) * 100
	if pct > 100 {
		pct = 100
	}
	return pct
}

// giftsFromLedger converts billing gift entries to display rows for the profile page.
func giftsFromLedger(gifts []billing.GiftEntry) []GiftRow {
	if len(gifts) == 0 {
		return nil
	}
	var rows []GiftRow
	for _, g := range gifts {
		dollar, cents := g.Amount.Dollars()
		var amount string
		if cents > 0 {
			amount = fmt.Sprintf("%d.%02d", dollar, cents)
		} else {
			amount = fmt.Sprintf("%d", dollar)
		}
		reason := g.Note
		if reason == "" {
			reason = "Credit gift"
		}
		rows = append(rows, GiftRow{
			Amount: amount,
			Reason: reason,
		})
	}
	return rows
}

// giftCreditsUSDFromLedger computes the total gift credits in USD from billing gift entries.
func giftCreditsUSDFromLedger(gifts []billing.GiftEntry) float64 {
	var total float64
	for _, g := range gifts {
		total += float64(g.Amount.Microcents()) / 1_000_000
	}
	return total
}

// Deprecated: computeSupportGift detects manual DB credit adjustments. Use giftsFromLedger instead.
//
// computeSupportGift detects manual DB credit adjustments by comparing the
// available credit to the sum of known credit sources (plan max + bonus grant).
// Any excess is treated as a support gift.
func computeSupportGift(shelleyCreditsAvailable, planMaxCredit, bonusGrantAmount float64) float64 {
	expected := planMaxCredit + bonusGrantAmount
	excess := shelleyCreditsAvailable - expected
	if excess > 0.5 {
		return excess
	}
	return 0
}

// giftsForUser returns the list of credit gifts to display on the profile page.
func giftsForUser(bonusRemaining, supportGiftUSD float64) []GiftRow {
	var gifts []GiftRow
	if bonusRemaining > 0 {
		gifts = append(gifts, GiftRow{
			Amount: fmt.Sprintf("%.0f", bonusRemaining),
			Reason: "Welcome bonus for upgrading to a paid plan",
		})
	}
	if supportGiftUSD > 0 {
		gifts = append(gifts, GiftRow{
			Amount: fmt.Sprintf("%.0f", supportGiftUSD),
			Reason: "exe.dev Support Gift",
		})
	}
	return gifts
}

// buildGiftRows combines the bonus gift row with gift entries from the billing ledger.
// bonusGrantAmount is the original one-time bonus (e.g. $100), not the remaining balance.
// We always show the full grant amount so the user knows they received it.
func buildGiftRows(bonusGrantAmount float64, giftEntries []billing.GiftEntry) []GiftRow {
	var rows []GiftRow
	if bonusGrantAmount > 0 {
		rows = append(rows, GiftRow{
			Amount: fmt.Sprintf("%.0f", bonusGrantAmount),
			Reason: "Welcome bonus for upgrading to a paid plan",
		})
	}
	rows = append(rows, giftsFromLedger(giftEntries)...)
	if len(rows) == 0 {
		return nil
	}
	return rows
}

// hasSignupGiftInLedger returns true if any gift entry has a GiftID prefixed
// with "signup:" (i.e. the signup bonus has been migrated to the billing ledger).
// When true, callers should zero out bonusGrantAmount and bonusRemaining to
// avoid double-counting the bonus in both the old flag path and the gift path.
func hasSignupGiftInLedger(entries []billing.GiftEntry) bool {
	for _, g := range entries {
		if strings.HasPrefix(g.GiftID, billing.GiftPrefixSignup+":") {
			return true
		}
	}
	return false
}
