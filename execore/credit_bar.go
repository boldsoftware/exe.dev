package execore

import "fmt"

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
	// supportGiftUSD is the detected support gift amount (manual DB credit adjustments).
	supportGiftUSD float64
}

// creditBarResult holds the computed credit bar display values.
type creditBarResult struct {
	totalRemainingPct float64
	usedCreditsUSD    float64
	usedBarPct        float64
	totalCapacity     float64
	monthlyAvailable  float64
	bonusRemaining    float64
	supportGiftUSD    float64
}

// computeCreditBar calculates a single unified bar.
//
// Capacity = planMax + bonusGrant + extra + supportGift (fixed denominator).
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

	supportGift := in.supportGiftUSD
	if supportGift < 0 {
		supportGift = 0
	}

	totalCapacity := in.planMaxCredit + in.bonusGrantAmount + in.extraCreditsUSD + supportGift
	if totalCapacity < 0 {
		totalCapacity = 0
	}

	remaining := monthlyAvailable + bonusRemaining + in.extraCreditsUSD + supportGift

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
		supportGiftUSD:    supportGift,
	}
}

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
