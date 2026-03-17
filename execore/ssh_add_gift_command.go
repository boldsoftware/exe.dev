package execore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"exe.dev/billing"
	"exe.dev/billing/tender"
	"exe.dev/exedb"
	"exe.dev/exemenu"
)

func (ss *SSHServer) handleAddGiftCommand(ctx context.Context, cc *exemenu.CommandContext) error {
	if len(cc.Args) < 2 {
		return cc.Errorf("usage: sudo-exe add-gift <userid-or-email> <amount-usd> [note]")
	}

	query := cc.Args[0]
	amountStr := cc.Args[1]
	note := ""
	if len(cc.Args) > 2 {
		note = strings.Join(cc.Args[2:], " ")
	}

	// Resolve user
	user, err := ss.resolveUserForCredits(ctx, query)
	if err != nil {
		return cc.Errorf("user not found: %s", query)
	}

	// Parse amount as USD float
	amountUSD, err := strconv.ParseFloat(amountStr, 64)
	if err != nil {
		return cc.Errorf("invalid amount: %s (must be a number in USD, e.g. 25.00)", amountStr)
	}
	if amountUSD <= 0 {
		return cc.Errorf("amount must be positive, got %.2f", amountUSD)
	}

	// Convert USD to tender.Value (amountUSD is in dollars, Mint takes cents)
	amount := tender.Mint(int64(amountUSD*100), 0)

	// Look up billing account
	account, err := withRxRes1(ss.server, ctx, (*exedb.Queries).GetAccountByUserID, user.UserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return cc.Errorf("user %s (%s) has no billing account", user.UserID, user.Email)
		}
		cc.WriteInternalError(ctx, "sudo-exe add-gift", err)
		return nil
	}

	// Generate gift ID
	giftID := fmt.Sprintf("ssh_gift:%s:%d", account.ID, time.Now().UnixNano())

	// Gift credits
	if err := ss.server.billing.GiftCredits(ctx, account.ID, &billing.GiftCreditsParams{
		Amount: amount,
		GiftID: giftID,
		Note:   note,
	}); err != nil {
		cc.WriteInternalError(ctx, "sudo-exe add-gift", err)
		return nil
	}

	cc.Writeln("")
	cc.Writeln("\033[1;32mGift credited successfully\033[0m")
	cc.Writeln("  User:      %s (%s)", user.UserID, user.Email)
	cc.Writeln("  Account:   %s", account.ID)
	cc.Writeln("  Amount:    $%.2f", amountUSD)
	cc.Writeln("  Gift ID:   %s", giftID)
	if note != "" {
		cc.Writeln("  Note:      %s", note)
	}
	cc.Writeln("")

	return nil
}
