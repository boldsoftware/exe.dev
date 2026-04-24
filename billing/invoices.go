package billing

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v85"
)

// InvoiceInfo holds the fields needed to display an invoice in the UI.
type InvoiceInfo struct {
	Description      string
	PlanName         string    // e.g. "Individual", "Team" — from first line item
	PeriodStart      time.Time // billing period start
	PeriodEnd        time.Time // billing period end
	Date             time.Time
	AmountPaid       int64  // cents — what was actually charged
	Subtotal         int64  // cents — sum of line items before discounts/credits
	Total            int64  // cents — after discounts, before credits
	DiscountAmount   int64  // cents — total discount applied (subtotal - total)
	CreditApplied    int64  // cents — how much existing credit was used to pay (positive)
	CreditGenerated  int64  // cents — how much new credit was generated (e.g. downgrade proration)
	Currency         string // e.g. "usd"
	Status           string // "paid", "open", "draft", "void", "uncollectible"
	HostedInvoiceURL string
	InvoicePDF       string
}

// ListInvoices returns the customer's invoices from the last 6 months.
func (m *Manager) ListInvoices(ctx context.Context, customerID string) ([]InvoiceInfo, error) {
	c := m.client()

	since := time.Now().AddDate(0, -6, 0)
	params := &stripe.InvoiceListParams{
		Customer:     stripe.String(customerID),
		CreatedRange: &stripe.RangeQueryParams{GreaterThanOrEqual: since.Unix()},
	}
	params.ListParams.Limit = stripe.Int64(12) // at most 12 invoices in 6 months

	var result []InvoiceInfo
	for inv, err := range c.V1Invoices.List(ctx, params).All(ctx) {
		if err != nil {
			return nil, fmt.Errorf("list invoices: %w", err)
		}
		// Only show paid and open invoices
		if inv.Status != stripe.InvoiceStatusPaid && inv.Status != stripe.InvoiceStatusOpen {
			continue
		}
		desc := inv.Description
		if desc == "" {
			// Build description from billing reason and period
			t := time.Unix(inv.PeriodEnd, 0).UTC()
			desc = "Subscription \u2014 " + t.Format("Jan 2006")
		}

		planName, periodStart, periodEnd := invoiceLineInfo(inv.Lines, inv.PeriodStart, inv.PeriodEnd)

		// Compute discount from TotalDiscountAmounts (explicit discount line items).
		// Credit applied = (subtotal - discount) - amountPaid.
		// Credit generated = negative subtotal (downgrade prorations credit the customer).
		var discountAmount int64
		for _, da := range inv.TotalDiscountAmounts {
			discountAmount += da.Amount
		}
		afterDiscount := inv.Subtotal - discountAmount
		creditApplied := max(afterDiscount-inv.AmountPaid, 0)
		creditGenerated := int64(0)
		if inv.Subtotal < 0 {
			creditGenerated = -inv.Subtotal
		}

		result = append(result, InvoiceInfo{
			Description:      desc,
			PlanName:         planName,
			PeriodStart:      periodStart,
			PeriodEnd:        periodEnd,
			Date:             time.Unix(inv.Created, 0).UTC(),
			AmountPaid:       inv.AmountPaid,
			Subtotal:         inv.Subtotal,
			Total:            inv.Total,
			DiscountAmount:   discountAmount,
			CreditApplied:    creditApplied,
			CreditGenerated:  creditGenerated,
			Currency:         string(inv.Currency),
			Status:           string(inv.Status),
			HostedInvoiceURL: inv.HostedInvoiceURL,
			InvoicePDF:       inv.InvoicePDF,
		})
	}
	return result, nil
}

// UpcomingInvoice returns a preview of the customer's next invoice, or nil if there isn't one.
func (m *Manager) UpcomingInvoice(ctx context.Context, customerID string) (*InvoiceInfo, error) {
	c := m.client()

	// CreatePreview requires a subscription ID — find the active one first.
	subID, err := m.activeSubscriptionID(ctx, c, customerID)
	if err != nil || subID == "" {
		return nil, nil //nolint:nilerr
	}

	inv, err := c.V1Invoices.CreatePreview(ctx, &stripe.InvoiceCreatePreviewParams{
		Customer:     stripe.String(customerID),
		Subscription: stripe.String(subID),
	})
	if err != nil {
		// No upcoming invoice is not an error.
		return nil, nil //nolint:nilerr
	}

	planName, periodStart, periodEnd := invoiceLineInfo(inv.Lines, inv.PeriodStart, inv.PeriodEnd)

	var discountAmount int64
	for _, da := range inv.TotalDiscountAmounts {
		discountAmount += da.Amount
	}
	afterDiscount := inv.Subtotal - discountAmount
	creditApplied := max(afterDiscount-inv.AmountDue, 0)
	creditGenerated := int64(0)
	if inv.Subtotal < 0 {
		creditGenerated = -inv.Subtotal
	}

	return &InvoiceInfo{
		Description:     "Upcoming",
		PlanName:        planName,
		PeriodStart:     periodStart,
		PeriodEnd:       periodEnd,
		Date:            time.Unix(inv.Created, 0).UTC(),
		AmountPaid:      inv.AmountDue,
		Subtotal:        inv.Subtotal,
		Total:           inv.Total,
		DiscountAmount:  discountAmount,
		CreditApplied:   creditApplied,
		CreditGenerated: creditGenerated,
		Currency:        string(inv.Currency),
		Status:          "upcoming",
	}, nil
}

// CustomerCreditBalance returns the customer's credit balance in cents.
// A positive return value means the customer has credit available.
// Stripe stores credit as a negative balance, so we negate it.
func (m *Manager) CustomerCreditBalance(ctx context.Context, customerID string) (int64, error) {
	c := m.client()
	customer, err := c.V1Customers.Retrieve(ctx, customerID, nil)
	if err != nil {
		return 0, fmt.Errorf("retrieve customer credit balance: %w", err)
	}
	// Stripe: negative balance = customer has credit.
	if customer.Balance < 0 {
		return -customer.Balance, nil
	}
	return 0, nil
}

// DiscountInfo holds display-safe details about a discount applied to a Stripe customer.
type DiscountInfo struct {
	CouponID         string  // Stripe coupon ID
	Name             string  // Display name of the coupon
	PercentOff       float64 // e.g. 50.0 for 50% off (0 if amount-based)
	AmountOffCents   int64   // e.g. 500 for $5.00 off (0 if percent-based)
	Duration         string  // "forever", "once", or "repeating"
	DurationInMonths int64   // Only set when Duration is "repeating"
}

// CustomerDiscount returns the active discount on a Stripe customer, if any.
// Returns (nil, nil) if the customer has no discount or doesn't exist.
func (m *Manager) CustomerDiscount(ctx context.Context, customerID string) (*DiscountInfo, error) {
	c := m.client()
	params := &stripe.CustomerRetrieveParams{}
	params.AddExpand("discount.source.coupon")
	customer, err := c.V1Customers.Retrieve(ctx, customerID, params)
	if err != nil {
		if isNotExists(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("retrieve customer for discount: %w", err)
	}
	if customer.Discount == nil || customer.Discount.Source == nil || customer.Discount.Source.Coupon == nil {
		return nil, nil
	}
	coupon := customer.Discount.Source.Coupon
	return &DiscountInfo{
		CouponID:         coupon.ID,
		Name:             coupon.Name,
		PercentOff:       coupon.PercentOff,
		AmountOffCents:   coupon.AmountOff,
		Duration:         string(coupon.Duration),
		DurationInMonths: coupon.DurationInMonths,
	}, nil
}

// invoiceLineInfo picks the best line item from an invoice for display.
// For proration invoices with multiple lines (e.g. a credit for the old plan
// and a charge for the new plan), it picks the line with the highest amount
// so we show the new plan rather than the credit.
func invoiceLineInfo(lines *stripe.InvoiceLineItemList, fallbackStart, fallbackEnd int64) (planName string, periodStart, periodEnd time.Time) {
	periodStart = time.Unix(fallbackStart, 0).UTC()
	periodEnd = time.Unix(fallbackEnd, 0).UTC()
	if lines == nil || len(lines.Data) == 0 {
		return "", periodStart, periodEnd
	}

	// Find the line item with the highest amount (the charge, not the credit).
	best := lines.Data[0]
	for _, li := range lines.Data[1:] {
		if li.Amount > best.Amount {
			best = li
		}
	}

	if best.Description != "" {
		planName = parseInvoiceLinePlanName(best.Description)
	}
	if best.Period != nil {
		periodStart = time.Unix(best.Period.Start, 0).UTC()
		periodEnd = time.Unix(best.Period.End, 0).UTC()
	}

	// Detect proration: multiple lines with at least one negative amount.
	if planName != "" && len(lines.Data) > 1 {
		for _, li := range lines.Data {
			if li.Amount < 0 {
				planName += " - Prorated"
				break
			}
		}
	}

	return planName, periodStart, periodEnd
}

// parseInvoiceLinePlanName extracts a clean plan name from a Stripe line item description.
// Handles regular lines like "1 × Individual Plan (at $20.00 / month)" → "Individual"
// and proration lines like "Remaining time on Individual Plan (XLarge) after 22 Apr 2026"
// → "Individual Plan (XLarge)".
func parseInvoiceLinePlanName(desc string) string {
	// Proration descriptions: "Remaining time on X after DATE" or "Unused time on X after DATE"
	for _, prefix := range []string{"Remaining time on ", "Unused time on "} {
		if strings.HasPrefix(desc, prefix) {
			desc = strings.TrimPrefix(desc, prefix)
			if i := strings.Index(desc, " after "); i >= 0 {
				desc = desc[:i]
			}
			return desc
		}
	}

	// Regular descriptions: "1 × Individual Plan (at $20.00 / month)"
	// Strip leading quantity: "1 × " or "1 x "
	if i := strings.Index(desc, "×"); i >= 0 {
		desc = strings.TrimSpace(desc[i+len("×"):])
	} else if i := strings.Index(desc, " x "); i >= 0 {
		desc = strings.TrimSpace(desc[i+3:])
	}
	// Strip trailing pricing: " (at $20.00 / month)"
	if i := strings.Index(desc, " (at "); i >= 0 {
		desc = strings.TrimSpace(desc[:i])
	}
	// Strip trailing " Plan" suffix for cleanliness
	desc = strings.TrimSuffix(desc, " Plan")
	return desc
}
