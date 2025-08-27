package exe

import (
	"fmt"
	"strings"

	"exe.dev/billing"
	"github.com/gliderlabs/ssh"
	"golang.org/x/term"
)

// SSH-specific billing presentation methods

func (ss *SSHServer) handleBillingCommand(s ssh.Session, publicKey string, args []string) {
	// Get user info
	user, err := ss.server.getUserByPublicKey(publicKey)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError: Failed to get user info: %v\033[0m\r\n", err)
		return
	}

	// Get allocation info
	alloc, err := ss.server.getUserAlloc(user.UserID)
	if err != nil || alloc == nil {
		fmt.Fprintf(s, "\033[1;31mError: No allocation found\033[0m\r\n")
		return
	}

	// Get billing info from the billing service
	billingInfo, err := ss.billing.GetBillingInfo(alloc.AllocID)
	if err != nil {
		fmt.Fprintf(s, "\033[1;31mError: Failed to get billing info: %v\033[0m\r\n", err)
		return
	}

	if billingInfo.HasBilling {
		ss.showBillingInfo(s, billingInfo)
	} else {
		ss.setupBilling(s, alloc.AllocID, user.Email)
	}
}

func (ss *SSHServer) showBillingInfo(s ssh.Session, billingInfo *billing.BillingInfo) {
	fmt.Fprintf(s, "\r\n\033[1;36mBilling Information:\033[0m\r\n\r\n")

	// Show current billing info
	if billingInfo.Email != "" {
		fmt.Fprintf(s, "  Email: \033[1m%s\033[0m\r\n", billingInfo.Email)
	}
	if billingInfo.StripeCustomerID != "" {
		fmt.Fprintf(s, "  Stripe Customer ID: \033[1m%s\033[0m\r\n", billingInfo.StripeCustomerID)
	}
	fmt.Fprintf(s, "  Status: \033[1;32mConfigured\033[0m\r\n\r\n")

	// Show options
	fmt.Fprintf(s, "\033[1mOptions:\033[0m\r\n")
	fmt.Fprintf(s, "  1. Update payment method\r\n")
	fmt.Fprintf(s, "  2. Update billing email\r\n")
	fmt.Fprintf(s, "  3. Delete billing info\r\n")
	fmt.Fprintf(s, "  4. Back to main menu\r\n\r\n")

	// Get user choice
	terminal := term.NewTerminal(s, "Choose an option (1-4): ")
	for {
		choice, err := terminal.ReadLine()
		if err != nil {
			fmt.Fprintf(s, "\r\nExiting billing menu.\r\n")
			return
		}

		switch strings.TrimSpace(choice) {
		case "1":
			ss.updatePaymentMethod(s, billingInfo.StripeCustomerID)
			return
		case "2":
			ss.updateBillingEmail(s, billingInfo)
			return
		case "3":
			ss.deleteBillingInfo(s, billingInfo)
			return
		case "4":
			fmt.Fprintf(s, "\r\nReturning to main menu.\r\n")
			return
		default:
			fmt.Fprintf(s, "\r\nInvalid choice. Please enter 1-4: ")
			terminal.SetPrompt("Choose an option (1-4): ")
		}
	}
}

func (ss *SSHServer) setupBilling(s ssh.Session, allocID, userEmail string) {
	fmt.Fprintf(s, "\r\n\033[1;33mBilling Setup\033[0m\r\n\r\n")
	fmt.Fprintf(s, "You need to set up billing information to continue using exe.dev.\r\n\r\n")

	terminal := term.NewTerminal(s, "")

	// Get billing email
	terminal.SetPrompt("Billing email (press Enter to use " + userEmail + "): ")
	billingEmail, err := terminal.ReadLine()
	if err != nil {
		fmt.Fprintf(s, "\r\nBilling setup cancelled.\r\n")
		return
	}

	if strings.TrimSpace(billingEmail) == "" {
		billingEmail = userEmail
	}

	// Validate email
	if !ss.billing.ValidateEmail(billingEmail) {
		fmt.Fprintf(s, "\r\n\033[1;31mInvalid email format.\033[0m\r\n")
		return
	}

	// Get credit card info
	fmt.Fprintf(s, "\r\nNow we need to verify your payment method.\r\n")
	fmt.Fprintf(s, "Please enter a credit card number to verify your payment method.\r\n")
	fmt.Fprintf(s, "For testing, you can use: \033[1m4242424242424242\033[0m (Visa test card)\r\n\r\n")

	terminal.SetPrompt("Credit card number: ")
	cardNumber, err := terminal.ReadLine()
	if err != nil {
		fmt.Fprintf(s, "\r\nBilling setup cancelled.\r\n")
		return
	}

	cardNumber = strings.ReplaceAll(strings.TrimSpace(cardNumber), " ", "")
	if len(cardNumber) < 13 {
		fmt.Fprintf(s, "\r\n\033[1;31mInvalid card number.\033[0m\r\n")
		return
	}

	// Get expiry month
	terminal.SetPrompt("Expiry month (MM): ")
	expMonth, err := terminal.ReadLine()
	if err != nil {
		fmt.Fprintf(s, "\r\nBilling setup cancelled.\r\n")
		return
	}

	// Get expiry year
	terminal.SetPrompt("Expiry year (YYYY): ")
	expYear, err := terminal.ReadLine()
	if err != nil {
		fmt.Fprintf(s, "\r\nBilling setup cancelled.\r\n")
		return
	}

	// Get CVC
	terminal.SetPrompt("CVC: ")
	cvc, err := terminal.ReadLine()
	if err != nil {
		fmt.Fprintf(s, "\r\nBilling setup cancelled.\r\n")
		return
	}

	// Setup billing using the billing service
	fmt.Fprintf(s, "\r\nProcessing payment method...\r\n")

	err = ss.billing.SetupBilling(allocID, billingEmail, cardNumber, expMonth, expYear, cvc)
	if err != nil {
		fmt.Fprintf(s, "\r\n\033[1;31mError setting up billing: %v\033[0m\r\n", err)
		return
	}

	fmt.Fprintf(s, "\r\n\033[1;32m✓ Billing setup completed successfully!\033[0m\r\n")
	fmt.Fprintf(s, "Your payment method has been verified and saved.\r\n\r\n")
}

func (ss *SSHServer) updatePaymentMethod(s ssh.Session, customerID string) {
	fmt.Fprintf(s, "\r\n\033[1;33mUpdate Payment Method\033[0m\r\n\r\n")

	terminal := term.NewTerminal(s, "")

	// Get new credit card info
	fmt.Fprintf(s, "Please enter your new payment method details.\r\n")
	fmt.Fprintf(s, "For testing, you can use: \033[1m4242424242424242\033[0m (Visa test card)\r\n\r\n")

	terminal.SetPrompt("Credit card number: ")
	cardNumber, err := terminal.ReadLine()
	if err != nil {
		fmt.Fprintf(s, "\r\nUpdate cancelled.\r\n")
		return
	}

	cardNumber = strings.ReplaceAll(strings.TrimSpace(cardNumber), " ", "")
	if len(cardNumber) < 13 {
		fmt.Fprintf(s, "\r\n\033[1;31mInvalid card number.\033[0m\r\n")
		return
	}

	// Get expiry details
	terminal.SetPrompt("Expiry month (MM): ")
	expMonth, err := terminal.ReadLine()
	if err != nil {
		fmt.Fprintf(s, "\r\nUpdate cancelled.\r\n")
		return
	}

	terminal.SetPrompt("Expiry year (YYYY): ")
	expYear, err := terminal.ReadLine()
	if err != nil {
		fmt.Fprintf(s, "\r\nUpdate cancelled.\r\n")
		return
	}

	terminal.SetPrompt("CVC: ")
	cvc, err := terminal.ReadLine()
	if err != nil {
		fmt.Fprintf(s, "\r\nUpdate cancelled.\r\n")
		return
	}

	fmt.Fprintf(s, "\r\nUpdating payment method...\r\n")

	// Update the payment method using billing service
	err = ss.billing.UpdatePaymentMethod(customerID, cardNumber, expMonth, expYear, cvc)
	if err != nil {
		fmt.Fprintf(s, "\r\n\033[1;31mError updating payment method: %v\033[0m\r\n", err)
		return
	}

	fmt.Fprintf(s, "\r\n\033[1;32m✓ Payment method updated successfully!\033[0m\r\n\r\n")
}

func (ss *SSHServer) updateBillingEmail(s ssh.Session, billingInfo *billing.BillingInfo) {
	fmt.Fprintf(s, "\r\n\033[1;33mUpdate Billing Email\033[0m\r\n\r\n")

	terminal := term.NewTerminal(s, "")
	terminal.SetPrompt("New billing email: ")

	newEmail, err := terminal.ReadLine()
	if err != nil {
		fmt.Fprintf(s, "\r\nUpdate cancelled.\r\n")
		return
	}

	newEmail = strings.TrimSpace(newEmail)
	if !ss.billing.ValidateEmail(newEmail) {
		fmt.Fprintf(s, "\r\n\033[1;31mInvalid email format.\033[0m\r\n")
		return
	}

	fmt.Fprintf(s, "\r\nUpdating billing email...\r\n")

	// Update billing email using billing service
	err = ss.billing.UpdateBillingEmail(billingInfo.AllocID, billingInfo.StripeCustomerID, newEmail)
	if err != nil {
		fmt.Fprintf(s, "\r\n\033[1;31mError updating billing email: %v\033[0m\r\n", err)
		return
	}

	fmt.Fprintf(s, "\r\n\033[1;32m✓ Billing email updated successfully!\033[0m\r\n\r\n")
}

func (ss *SSHServer) deleteBillingInfo(s ssh.Session, billingInfo *billing.BillingInfo) {
	fmt.Fprintf(s, "\r\n\033[1;33mDelete Billing Information\033[0m\r\n\r\n")
	fmt.Fprintf(s, "\033[1;31mWarning: This will remove all billing information from your account.\033[0m\r\n")
	fmt.Fprintf(s, "You will need to set up billing again to continue using exe.dev.\r\n\r\n")

	terminal := term.NewTerminal(s, "")
	terminal.SetPrompt("Are you sure? Type 'yes' to confirm: ")

	confirmation, err := terminal.ReadLine()
	if err != nil {
		fmt.Fprintf(s, "\r\nOperation cancelled.\r\n")
		return
	}

	if strings.ToLower(strings.TrimSpace(confirmation)) != "yes" {
		fmt.Fprintf(s, "\r\nOperation cancelled.\r\n")
		return
	}

	fmt.Fprintf(s, "\r\nDeleting billing information...\r\n")

	// Delete billing info using billing service
	err = ss.billing.DeleteBillingInfo(billingInfo.AllocID)
	if err != nil {
		fmt.Fprintf(s, "\r\n\033[1;31mError deleting billing info: %v\033[0m\r\n", err)
		return
	}

	fmt.Fprintf(s, "\r\n\033[1;32m✓ Billing information deleted successfully!\033[0m\r\n")
	fmt.Fprintf(s, "You can set up billing again using the 'billing' command.\r\n\r\n")
}
