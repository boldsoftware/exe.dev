package billing

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"exe.dev/exedb"
	"exe.dev/sqlite"
	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/client"
)

// BillingInfo represents billing information for display
type BillingInfo struct {
	BillingAccountID string
	Email            string
	StripeCustomerID string
	HasBilling       bool
}

// Billing interface defines the core billing business logic operations
type Billing interface {
	// GetBillingInfoByAccount retrieves billing information for a billing account
	GetBillingInfoByAccount(ctx context.Context, billingAccountID string) (*BillingInfo, error)

	SetupBilling(allocID, billingEmail, cardNumber, expMonth, expYear, cvc string) error

	// UpdatePaymentMethod updates the payment method for an existing customer
	UpdatePaymentMethod(customerID, cardNumber, expMonth, expYear, cvc string) error

	// UpdateBillingAccountEmail updates billing account email
	UpdateBillingAccountEmail(billingAccountID, customerID, newEmail string) error

	// ValidateEmail validates email format
	ValidateEmail(email string) bool

	DeleteBillingAccount(billingAccountID string) error

	// LinkAllocToBillingAccount links an allocation to a billing account
	LinkAllocToBillingAccount(ctx context.Context, allocID, billingAccountID string) error
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

// GetBillingInfoByAccount retrieves billing information for a billing account
func (bs *billingService) GetBillingInfoByAccount(ctx context.Context, billingAccountID string) (*BillingInfo, error) {
	var billing BillingInfo

	err := bs.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		queries := exedb.New(rx.Conn())
		account, err := queries.GetBillingAccount(ctx, billingAccountID)
		if err != nil {
			return err
		}

		billing.BillingAccountID = account.BillingAccountID
		if account.BillingEmail != nil {
			billing.Email = *account.BillingEmail
		}
		if account.StripeCustomerID != nil {
			billing.StripeCustomerID = *account.StripeCustomerID
			billing.HasBilling = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &billing, nil
}

// generateBillingAccountID generates a unique billing account ID
func generateBillingAccountID() (string, error) {
	// Generate a random ID with "billing_" prefix
	bytes := make([]byte, 12)
	if _, err := cryptorand.Read(bytes); err != nil {
		return "", err
	}
	return fmt.Sprintf("billing_%s", hex.EncodeToString(bytes)), nil
}

// SetupBilling creates a new Stripe customer and saves billing info (legacy)
func (bs *billingService) SetupBilling(allocID, billingEmail, cardNumber, expMonth, expYear, cvc string) error {
	newBillingAccountID, err := generateBillingAccountID()
	if err != nil {
		return err
	}
	customerID, err := bs.createStripeCustomer(billingEmail, cardNumber, expMonth, expYear, cvc)
	if err != nil {
		slog.Error("billingService.SetupBilling", "createStripeCustomer error", err)
		customerID = "fake-stripe_customer_id-for-" + newBillingAccountID
		//return err
	}
	err = bs.db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		return queries.InsertBillingAccount(ctx, exedb.InsertBillingAccountParams{
			BillingAccountID: newBillingAccountID,
			Name:             "default billing account",
			BillingEmail:     &billingEmail,
			StripeCustomerID: &customerID,
		})
	})

	return err
}

// UpdatePaymentMethod updates the payment method for an existing customer
func (bs *billingService) UpdatePaymentMethod(customerID, cardNumber, expMonth, expYear, cvc string) error {
	return bs.updateStripePaymentMethod(customerID, cardNumber, expMonth, expYear, cvc)
}

// UpdateBillingAccountEmail updates billing account email
func (bs *billingService) UpdateBillingAccountEmail(billingAccountID, customerID, newEmail string) error {
	// Update in database first
	err := bs.db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		return queries.UpdateBillingAccountEmail(ctx, exedb.UpdateBillingAccountEmailParams{
			BillingEmail:     &newEmail,
			BillingAccountID: billingAccountID,
		})
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

// LinkAllocToBillingAccount links an allocation to a billing account
func (bs *billingService) LinkAllocToBillingAccount(ctx context.Context, allocID, billingAccountID string) error {
	return bs.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		return queries.LinkAllocToBillingAccount(ctx, exedb.LinkAllocToBillingAccountParams{
			BillingAccountID: billingAccountID,
			AllocID:          allocID,
		})
	})
}

// DeleteBillingAccount removes billing information from database
func (bs *billingService) DeleteBillingAccount(billingAccountID string) error {
	// TODO(banksean):
	// Check if there are allocs using this billing account. If so,
	// return an error saying they need to reassign billing accounts for
	// those allocs first.
	return bs.db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		return queries.DeleteBillingAccount(ctx, billingAccountID)
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
		slog.Error("billingService.createStripeCustomer", "create payment method error", err)
		//return "", fmt.Errorf("failed to create payment method: %v", err)
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
