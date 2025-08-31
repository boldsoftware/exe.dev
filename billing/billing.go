package billing

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"

	"exe.dev/sqlite"
	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/client"
)

// BillingInfo represents billing information for display
type BillingInfo struct {
	AllocID          string
	Email            string
	StripeCustomerID string
	HasBilling       bool
}

// Billing interface defines the core billing business logic operations
type Billing interface {
	// GetBillingInfo retrieves current billing information for an allocation
	GetBillingInfo(allocID string) (*BillingInfo, error)

	// SetupBilling creates a new Stripe customer and saves billing info
	SetupBilling(allocID, billingEmail, cardNumber, expMonth, expYear, cvc string) error

	// UpdatePaymentMethod updates the payment method for an existing customer
	UpdatePaymentMethod(customerID, cardNumber, expMonth, expYear, cvc string) error

	// UpdateBillingEmail updates the billing email in database and Stripe
	UpdateBillingEmail(allocID, customerID, newEmail string) error

	// DeleteBillingInfo removes billing information from database
	DeleteBillingInfo(allocID string) error

	// ValidateEmail validates email format
	ValidateEmail(email string) bool
}

// billingService implements the Billing interface
type billingService struct {
	db     *sqlite.DB
	client *client.API
}

// New creates a new BillingService
func New(db *sqlite.DB) Billing {
	return &billingService{
		db:     db,
		client: createStripeClient(),
	}
}

// NewWithClient creates a new BillingService with custom client
func NewWithClient(db *sqlite.DB, stripeClient *client.API) Billing {
	return &billingService{
		db:     db,
		client: stripeClient,
	}
}

// createStripeClient creates a Stripe client configured for mock or real service
func createStripeClient() *client.API {
	config := &stripe.BackendConfig{}

	mockURL := os.Getenv("STRIPE_MOCK_URL")
	if mockURL != "" {
		config.URL = stripe.String(mockURL)
	}

	return client.New(stripe.Key, &stripe.Backends{
		API: stripe.GetBackendWithConfig(stripe.APIBackend, config),
	})
}

// GetBillingInfo retrieves current billing information for an allocation
func (bs *billingService) GetBillingInfo(allocID string) (*BillingInfo, error) {
	var billing BillingInfo
	var emailNull, customerIDNull sql.NullString

	err := bs.db.Rx(context.Background(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`
			SELECT billing_email, stripe_customer_id 
			FROM allocs WHERE alloc_id = ?`, allocID).Scan(&emailNull, &customerIDNull)
	})
	if err != nil {
		return nil, err
	}

	billing.AllocID = allocID
	if emailNull.Valid {
		billing.Email = emailNull.String
	}
	if customerIDNull.Valid {
		billing.StripeCustomerID = customerIDNull.String
		billing.HasBilling = true
	}

	return &billing, nil
}

// SetupBilling creates a new Stripe customer and saves billing info
func (bs *billingService) SetupBilling(allocID, billingEmail, cardNumber, expMonth, expYear, cvc string) error {
	customerID, err := bs.createStripeCustomer(billingEmail, cardNumber, expMonth, expYear, cvc)
	if err != nil {
		return err
	}

	return bs.updateAllocBilling(allocID, customerID, billingEmail)
}

// UpdatePaymentMethod updates the payment method for an existing customer
func (bs *billingService) UpdatePaymentMethod(customerID, cardNumber, expMonth, expYear, cvc string) error {
	return bs.updateStripePaymentMethod(customerID, cardNumber, expMonth, expYear, cvc)
}

// UpdateBillingEmail updates the billing email in database and Stripe
func (bs *billingService) UpdateBillingEmail(allocID, customerID, newEmail string) error {
	// Update in database first
	err := bs.db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec("UPDATE allocs SET billing_email = ? WHERE alloc_id = ?", newEmail, allocID)
		return err
	})
	if err != nil {
		return err
	}

	// Update in Stripe if customerID is provided
	if customerID != "" {
		return bs.updateStripeCustomerEmail(customerID, newEmail)
	}

	return nil
}

// DeleteBillingInfo removes billing information from database
func (bs *billingService) DeleteBillingInfo(allocID string) error {
	return bs.db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec("UPDATE allocs SET stripe_customer_id = NULL, billing_email = NULL WHERE alloc_id = ?", allocID)
		return err
	})
}

// ValidateEmail validates email format (simple validation)
func (bs *billingService) ValidateEmail(email string) bool {
	return strings.Contains(email, "@") && strings.Contains(email, ".")
}

// Stripe integration helper methods for BillingService

func (bs *billingService) createStripeCustomer(email, cardNumber, expMonth, expYear, cvc string) (string, error) {
	// Convert expiry month and year to int64
	expMonthInt, err := strconv.ParseInt(expMonth, 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid expiry month: %v", err)
	}
	expYearInt, err := strconv.ParseInt(expYear, 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid expiry year: %v", err)
	}

	// Create payment method
	pmParams := &stripe.PaymentMethodParams{
		Type: stripe.String("card"),
		Card: &stripe.PaymentMethodCardParams{
			Number:   stripe.String(cardNumber),
			ExpMonth: stripe.Int64(expMonthInt),
			ExpYear:  stripe.Int64(expYearInt),
			CVC:      stripe.String(cvc),
		},
	}

	pm, err := bs.client.PaymentMethods.New(pmParams)
	if err != nil {
		return "", fmt.Errorf("failed to create payment method: %v", err)
	}

	// Create customer
	customerParams := &stripe.CustomerParams{
		Email:         stripe.String(email),
		PaymentMethod: stripe.String(pm.ID),
		InvoiceSettings: &stripe.CustomerInvoiceSettingsParams{
			DefaultPaymentMethod: stripe.String(pm.ID),
		},
	}

	cust, err := bs.client.Customers.New(customerParams)
	if err != nil {
		return "", fmt.Errorf("failed to create customer: %v", err)
	}

	// Attach payment method to customer
	pmAttachParams := &stripe.PaymentMethodAttachParams{
		Customer: stripe.String(cust.ID),
	}
	_, err = bs.client.PaymentMethods.Attach(pm.ID, pmAttachParams)
	if err != nil {
		return "", fmt.Errorf("failed to attach payment method: %v", err)
	}

	return cust.ID, nil
}

func (bs *billingService) updateStripePaymentMethod(customerID, cardNumber, expMonth, expYear, cvc string) error {
	// Convert expiry month and year to int64
	expMonthInt, err := strconv.ParseInt(expMonth, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid expiry month: %v", err)
	}
	expYearInt, err := strconv.ParseInt(expYear, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid expiry year: %v", err)
	}

	// Create new payment method
	pmParams := &stripe.PaymentMethodParams{
		Type: stripe.String("card"),
		Card: &stripe.PaymentMethodCardParams{
			Number:   stripe.String(cardNumber),
			ExpMonth: stripe.Int64(expMonthInt),
			ExpYear:  stripe.Int64(expYearInt),
			CVC:      stripe.String(cvc),
		},
	}

	pm, err := bs.client.PaymentMethods.New(pmParams)
	if err != nil {
		return fmt.Errorf("failed to create payment method: %v", err)
	}

	// Attach to customer
	pmAttachParams := &stripe.PaymentMethodAttachParams{
		Customer: stripe.String(customerID),
	}
	_, err = bs.client.PaymentMethods.Attach(pm.ID, pmAttachParams)
	if err != nil {
		return fmt.Errorf("failed to attach payment method: %v", err)
	}

	// Update customer default payment method
	customerUpdateParams := &stripe.CustomerParams{
		InvoiceSettings: &stripe.CustomerInvoiceSettingsParams{
			DefaultPaymentMethod: stripe.String(pm.ID),
		},
	}
	_, err = bs.client.Customers.Update(customerID, customerUpdateParams)
	if err != nil {
		return fmt.Errorf("failed to update customer default payment method: %v", err)
	}

	return nil
}

func (bs *billingService) updateStripeCustomerEmail(customerID, email string) error {
	customerUpdateParams := &stripe.CustomerParams{
		Email: stripe.String(email),
	}
	_, err := bs.client.Customers.Update(customerID, customerUpdateParams)
	if err != nil {
		return fmt.Errorf("failed to update customer email: %v", err)
	}
	return nil
}

func (bs *billingService) updateAllocBilling(allocID, customerID, billingEmail string) error {
	return bs.db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(
			"UPDATE allocs SET stripe_customer_id = ?, billing_email = ? WHERE alloc_id = ?",
			customerID, billingEmail, allocID,
		)
		return err
	})
}
