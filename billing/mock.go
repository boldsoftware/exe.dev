package billing

import "context"

// MockService implements the Billing interface for testing
type MockService struct {
	BillingInfo *BillingInfo
	Error       error
}

var _ Billing = &MockService{}

func (m *MockService) GetBillingInfo(ctx context.Context, allocID string) (*BillingInfo, error) {
	if m.Error != nil {
		return nil, m.Error
	}
	return m.BillingInfo, nil
}

func (m *MockService) SetupBilling(allocID, billingEmail, cardNumber, expMonth, expYear, cvc string) error {
	return m.Error
}

func (m *MockService) UpdatePaymentMethod(customerID, cardNumber, expMonth, expYear, cvc string) error {
	return m.Error
}

func (m *MockService) UpdateBillingEmail(allocID, customerID, newEmail string) error {
	return m.Error
}

func (m *MockService) DeleteBillingInfo(allocID string) error {
	return m.Error
}

func (m *MockService) ValidateEmail(email string) bool {
	return true // For testing, assume all emails are valid
}
