// Package billing provides subscription and payment management for exe.dev accounts.
package billing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/stripe/stripe-go/v82"
)

// TestAPIKey is the Stripe test API key. It is safe to check into source code
// and easy to revoke should someone want to spam our test account.
const TestAPIKey = "sk_test_51SjuBkGpGU0hqBfTJ2Bkl1cKcayvyCEpugA9WfvYFQLIV6qkfmM2lcgicYfG6yJUsDXdmlYx217xYE349efIFwAx00OiQwF5jA"

var stripeKey = os.Getenv("STRIPE_API_KEY")

const DefaultPlan = "individual"

var priceIDCache sync.Map // lookup key -> price ID

// Manager handles billing operations.
type Manager struct {
	// APIKey specifies the Stripe API key to use for requests.
	// If empty, it will use any of the following in order of precedence:
	//
	//   1. The STRIPE_API_KEY environment variable
	//   2. The sandboxAPIKey
	APIKey string
}

// SubscribeParams contains the parameters for subscribing an account to a plan.
type SubscribeParams struct {
	// Email is the account's email address.
	Email string

	// Plan is the Stripe price lookup key for the plan the account is
	// signing up for. If empty, DefaultPlan is used.
	//
	// Lookup keys can be found in the Stripe dashboard under
	// https://dashboard.stripe.com/products.
	Plan string

	// SuccessURL is the URL to redirect to after successful checkout.
	SuccessURL string

	// CancelURL is the URL to redirect to if checkout is canceled.
	CancelURL string

	// TrialEnd specifies when the trial period ends. If set, billing will
	// not start until this time. If zero, billing starts immediately.
	TrialEnd time.Time
}

// Profile contains account profile information.
type Profile struct {
	Email string
}

func (m *Manager) client() *stripe.Client {
	apiKey := m.APIKey
	if apiKey == "" {
		apiKey = stripeKey
	}
	return stripe.NewClient(apiKey)
}

// Subscribe generates a payment link for subscribing an account to a plan.
//
// It returns a payment link URL for the account to complete the subscription.
func (m *Manager) Subscribe(ctx context.Context, exeAccountID string, p *SubscribeParams) (paymentLink string, _ error) {
	if p == nil {
		p = &SubscribeParams{}
	}

	c := m.client()

	plan := p.Plan
	if plan == "" {
		plan = DefaultPlan
	}

	// Look up the price by its lookup key
	priceID, err := m.lookupPriceID(ctx, c, plan)
	if err != nil {
		return "", fmt.Errorf("lookup price %q: %w", plan, err)
	}

	// Create Stripe customer record if one doesn't exist.
	// A little secret Stripe does not document well, or expose in their SDK:
	// You can bring your own customer IDs!
	// https://stripe.com/docs/api/customers/create#create_customer-id
	custParams := &stripe.CustomerCreateParams{
		Email: &p.Email,
	}
	custParams.AddExtra("id", exeAccountID)

	_, err = c.V1Customers.Create(ctx, custParams)
	if err != nil {
		var stripeErr *stripe.Error
		if !errors.As(err, &stripeErr) || stripeErr.Code != stripe.ErrorCodeResourceAlreadyExists {
			return "", err
		}
	}

	params := &stripe.CheckoutSessionCreateParams{
		Customer: &exeAccountID,
		Mode:     stripe.String("subscription"),
		LineItems: []*stripe.CheckoutSessionCreateLineItemParams{
			{
				Price:    &priceID,
				Quantity: stripe.Int64(1),
			},
		},
		SuccessURL: &p.SuccessURL,
		CancelURL:  &p.CancelURL,
	}

	// Set trial end if specified (defers first billing until that date)
	if !p.TrialEnd.IsZero() {
		params.SubscriptionData = &stripe.CheckoutSessionCreateSubscriptionDataParams{
			TrialEnd: stripe.Int64(p.TrialEnd.Unix()),
		}
	}

	sess, err := c.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return "", err
	}

	return sess.URL, nil
}

// lookupPriceID finds the price ID for a given lookup key, caching results.
func (m *Manager) lookupPriceID(ctx context.Context, c *stripe.Client, lookupKey string) (string, error) {
	if v, ok := priceIDCache.Load(lookupKey); ok {
		return v.(string), nil
	}

	var priceID string
	for price, err := range c.V1Prices.List(ctx, &stripe.PriceListParams{
		LookupKeys: []*string{&lookupKey},
		Active:     stripe.Bool(true),
	}) {
		if err != nil {
			return "", err
		}
		priceID = price.ID
		break
	}
	if priceID == "" {
		return "", fmt.Errorf("no active price found with lookup key %q", lookupKey)
	}

	priceIDCache.Store(lookupKey, priceID)
	return priceID, nil
}

// UpdateProfile updates the account's profile information.
func (m *Manager) UpdateProfile(ctx context.Context, exeAccountID string, p *Profile) error {
	if p == nil {
		return nil
	}

	c := m.client()

	params := &stripe.CustomerUpdateParams{}
	if p.Email != "" {
		params.Email = &p.Email
	}

	_, err := c.V1Customers.Update(ctx, exeAccountID, params)
	return err
}
