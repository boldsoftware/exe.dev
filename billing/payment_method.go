package billing

import (
	"context"
	"fmt"
	"strings"

	"github.com/stripe/stripe-go/v85"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// PaymentMethodInfo holds display-safe payment method details fetched from Stripe.
type PaymentMethodInfo struct {
	Type         string // "card", "link", "paypal", etc.
	Brand        string // For cards: "visa", "mastercard", etc.
	Last4        string // Last 4 digits for cards, empty otherwise
	ExpMonth     int    // 1-12 for cards, 0 otherwise
	ExpYear      int    // 4-digit year for cards, 0 otherwise
	Email        string // For Link/PayPal: the associated email
	DisplayLabel string // e.g. "Visa •••• 4242" or "Link (user@example.com)"
}

// GetPaymentMethod fetches the payment method used for the customer's subscription from Stripe.
// Returns (nil, nil) if no payment method is set or the customer doesn't exist.
//
// Stripe Checkout sets the payment method on the subscription (not on the
// customer-level invoice_settings.default_payment_method), so we check the
// subscription first. This ensures that when a user pays with a card that
// isn't their customer-level default, we show the correct one.
func (m *Manager) GetPaymentMethod(ctx context.Context, billingID string) (*PaymentMethodInfo, error) {
	c := m.client()

	// Prefer the subscription's payment method — this is the one actually
	// being charged for the subscription. Check active, trialing, and
	// past_due statuses (all represent a live subscription).
	for _, status := range []string{"active", "trialing", "past_due"} {
		subParams := &stripe.SubscriptionListParams{
			Customer: &billingID,
			Status:   new(status),
		}
		subParams.AddExpand("data.default_payment_method")
		for sub, err := range c.V1Subscriptions.List(ctx, subParams).All(ctx) {
			if err != nil {
				return nil, fmt.Errorf("list %s subscriptions for payment method: %w", status, err)
			}
			if sub.DefaultPaymentMethod != nil {
				return extractPaymentMethodInfo(sub.DefaultPaymentMethod), nil
			}
		}
	}

	// Fall back to the customer-level default payment method.
	params := &stripe.CustomerRetrieveParams{}
	params.AddExpand("invoice_settings.default_payment_method")

	customer, err := c.V1Customers.Retrieve(ctx, billingID, params)
	if err != nil {
		if isNotExists(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("retrieve customer for payment method: %w", err)
	}

	if customer.InvoiceSettings != nil && customer.InvoiceSettings.DefaultPaymentMethod != nil {
		return extractPaymentMethodInfo(customer.InvoiceSettings.DefaultPaymentMethod), nil
	}

	return nil, nil
}

func extractPaymentMethodInfo(pm *stripe.PaymentMethod) *PaymentMethodInfo {
	if pm == nil {
		return nil
	}
	info := &PaymentMethodInfo{Type: string(pm.Type)}
	switch pm.Type {
	case stripe.PaymentMethodTypeCard:
		if pm.Card != nil {
			info.Brand = string(pm.Card.Brand)
			info.Last4 = pm.Card.Last4
			info.ExpMonth = int(pm.Card.ExpMonth)
			info.ExpYear = int(pm.Card.ExpYear)
			info.DisplayLabel = formatCardLabel(info.Brand, info.Last4)
		}
	case stripe.PaymentMethodTypeLink:
		if pm.Link != nil && pm.Link.Email != "" {
			info.Email = pm.Link.Email
			info.DisplayLabel = fmt.Sprintf("Link (%s)", pm.Link.Email)
		} else {
			info.DisplayLabel = "Link"
		}
	case stripe.PaymentMethodTypePaypal:
		if pm.Paypal != nil && pm.Paypal.PayerEmail != "" {
			info.Email = pm.Paypal.PayerEmail
			info.DisplayLabel = fmt.Sprintf("PayPal (%s)", pm.Paypal.PayerEmail)
		} else {
			info.DisplayLabel = "PayPal"
		}
	default:
		info.DisplayLabel = formatPaymentTypeLabel(string(pm.Type))
	}
	return info
}

func formatCardLabel(brand, last4 string) string {
	tc := cases.Title(language.English)
	brandDisplay := tc.String(brand)
	switch brand {
	case "amex":
		brandDisplay = "American Express"
	case "diners":
		brandDisplay = "Diners Club"
	case "jcb":
		brandDisplay = "JCB"
	}
	if last4 != "" {
		return fmt.Sprintf("%s •••• %s", brandDisplay, last4)
	}
	return brandDisplay
}

func formatPaymentTypeLabel(pmType string) string {
	tc := cases.Title(language.English)
	return tc.String(strings.ReplaceAll(pmType, "_", " "))
}
