package execore

// creditBarInput holds the inputs needed to compute credit bar display values.
type creditBarInput struct {
	// shelleyCreditsAvailable is the user's current available credit in USD (monthly + bonus).
	shelleyCreditsAvailable float64
	// planMaxCredit is the base monthly plan capacity in USD (e.g. $20).
	planMaxCredit float64
	// bonusRemaining is the portion of available credit attributable to the upgrade bonus.
	// Computed as max(0, available - planMax) when bonus was granted, 0 otherwise.
	bonusRemaining float64
	// extraCreditsUSD is the user's purchased extra credit balance in USD.
	extraCreditsUSD float64
}

// creditBarResult holds the computed credit bar display values.
type creditBarResult struct {
	monthlyBarPct     float64
	bonusBarPct       float64
	extraBarPct       float64
	totalRemainingPct float64
	usedCreditsUSD    float64
	usedBarPct        float64
	totalCapacity     float64
	monthlyAvailable  float64
	bonusRemaining    float64
}

// computeCreditBar calculates the stacked bar percentages for the credit display.
//
// The bar segments (left to right): used | monthly | bonus | extra
//
// The denominator smoothly shrinks as the bonus drains, avoiding a cliff
// when the bonus is fully exhausted:
//
//	capacity = planMax + bonusRemaining + extra
func computeCreditBar(in creditBarInput) creditBarResult {
	// Split available into monthly and bonus portions.
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

	// Smooth denominator: planMax + bonus + extra.
	totalCapacity := in.planMaxCredit + bonusRemaining + in.extraCreditsUSD
	if totalCapacity < 0 {
		totalCapacity = 0
	}

	var monthlyBarPct, bonusBarPct, extraBarPct, totalRemainingPct float64
	if totalCapacity > 0 {
		monthlyBarPct = (monthlyAvailable / totalCapacity) * 100
		bonusBarPct = (bonusRemaining / totalCapacity) * 100
		extraBarPct = (in.extraCreditsUSD / totalCapacity) * 100
		totalRemainingPct = ((monthlyAvailable + bonusRemaining + in.extraCreditsUSD) / totalCapacity) * 100
	}
	if totalRemainingPct < 0 {
		totalRemainingPct = 0
	} else if totalRemainingPct > 100 {
		totalRemainingPct = 100
	}
	if monthlyBarPct < 0 {
		monthlyBarPct = 0
	}

	// "Used" only applies to the monthly portion — bonus drains separately.
	usedCreditsUSD := in.planMaxCredit - monthlyAvailable
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
		monthlyBarPct:     monthlyBarPct,
		bonusBarPct:       bonusBarPct,
		extraBarPct:       extraBarPct,
		totalRemainingPct: totalRemainingPct,
		usedCreditsUSD:    usedCreditsUSD,
		usedBarPct:        usedBarPct,
		totalCapacity:     totalCapacity,
		monthlyAvailable:  monthlyAvailable,
		bonusRemaining:    bonusRemaining,
	}
}
