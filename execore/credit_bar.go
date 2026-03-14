package execore

// creditBarInput holds the inputs needed to compute credit bar display values.
type creditBarInput struct {
	// shelleyCreditsAvailable is the user's current available monthly/bonus credit in USD.
	shelleyCreditsAvailable float64
	// shelleyCreditsMax is the fixed capacity (plan max, or plan max + bonus if applicable).
	shelleyCreditsMax float64
	// extraCreditsUSD is the user's purchased extra credit balance in USD.
	extraCreditsUSD float64
}

// creditBarResult holds the computed credit bar display values.
type creditBarResult struct {
	monthlyBarPct     float64
	extraBarPct       float64
	totalRemainingPct float64
	usedCreditsUSD    float64
	usedBarPct        float64
	totalCapacity     float64
}

// computeCreditBar calculates the stacked bar percentages for the credit display.
func computeCreditBar(in creditBarInput) creditBarResult {
	monthlyCapacity := in.shelleyCreditsMax
	totalCapacity := monthlyCapacity + in.extraCreditsUSD

	var monthlyBarPct, extraBarPct, totalRemainingPct float64
	if totalCapacity > 0 {
		monthlyBarPct = (in.shelleyCreditsAvailable / totalCapacity) * 100
		extraBarPct = (in.extraCreditsUSD / totalCapacity) * 100
		totalRemainingPct = ((in.shelleyCreditsAvailable + in.extraCreditsUSD) / totalCapacity) * 100
	}
	if totalRemainingPct < 0 {
		totalRemainingPct = 0
	} else if totalRemainingPct > 100 {
		totalRemainingPct = 100
	}
	if monthlyBarPct < 0 {
		monthlyBarPct = 0
	}

	usedCreditsUSD := monthlyCapacity - in.shelleyCreditsAvailable
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
		extraBarPct:       extraBarPct,
		totalRemainingPct: totalRemainingPct,
		usedCreditsUSD:    usedCreditsUSD,
		usedBarPct:        usedBarPct,
		totalCapacity:     totalCapacity,
	}
}
