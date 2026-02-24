package exeprox

import (
	"context"
	"errors"
	"time"

	"exe.dev/billing/tender"
	"exe.dev/llmgateway"
	proxyapi "exe.dev/pkg/api/exe/proxy/v1"
)

// ProxyLLMGatewayData implements [llmgateway.GatewayData]
// by sending database requests over grpc to exed.
type ProxyLLMGatewayData struct {
	data   ExeproxData
	client proxyapi.ProxyInfoServiceClient
	boxes  *boxesData
	users  *usersData
}

// BoxCreator implements [llmgateway.GatewayData.BoxCreator].
func (gd *ProxyLLMGatewayData) BoxCreator(ctx context.Context, boxName string) (string, bool, error) {
	data, exists, err := gd.boxes.lookup(ctx, gd.data, boxName)
	if err != nil {
		return "", false, err
	}
	if !exists {
		return "", false, nil
	}
	return data.CreatedByUserID, true, nil
}

// CheckAndRefreshCredit implements
// [llmgateway.GatewayData.CheckAndRefreshCredit].
func (gd *ProxyLLMGatewayData) CheckAndRefreshCredit(ctx context.Context, userID string, now time.Time) (*llmgateway.CreditInfo, error) {
	if !now.IsZero() {
		return nil, errors.New("non-zero time")
	}
	resp, err := gd.client.CheckAndRefreshLLMCredit(ctx, &proxyapi.CheckAndRefreshLLMCreditRequest{
		UserID: userID,
	})
	if err != nil {
		return nil, err
	}
	return protoCreditInfoToGateway(resp.CreditInfo), nil
}

// TopUpOnBillingUpgrade implements
// [llmgateway.GatewayData.TopUpOnBillingUpgrade].
func (gd *ProxyLLMGatewayData) TopUpOnBillingUpgrade(ctx context.Context, userID string, now time.Time) error {
	if !now.IsZero() {
		return errors.New("non-zero time")
	}
	_, err := gd.client.TopUpOnLLMBillingUpgrade(ctx, &proxyapi.TopUpOnLLMBillingUpgradeRequest{
		UserID: userID,
	})
	return err
}

// DebitCredit implements [llmgateway.GatewayData.DebitCredit].
func (gd *ProxyLLMGatewayData) DebitCredit(ctx context.Context, userID string, costUSD float64, now time.Time) (*llmgateway.CreditInfo, error) {
	if !now.IsZero() {
		return nil, errors.New("non-zero time")
	}
	resp, err := gd.client.LLMDebitCredit(ctx, &proxyapi.LLMDebitCreditRequest{
		UserID:  userID,
		CostUsd: costUSD,
	})
	if err != nil {
		return nil, err
	}
	return protoCreditInfoToGateway(resp.CreditInfo), nil
}

// AccountIDForUser implements [llmgateway.GatewayData.AccountIDForUser].
func (gd *ProxyLLMGatewayData) AccountIDForUser(ctx context.Context, userID string) (string, bool, error) {
	ud, exists, err := gd.users.lookup(ctx, gd.data, userID)
	if err != nil {
		return "", false, err
	}
	if !exists || ud.accountID == "" {
		return "", false, nil
	}
	return ud.accountID, true, nil
}

// UseCredits implements [llmgateway.GatewayData.UseCredits].
func (gd *ProxyLLMGatewayData) UseCredits(ctx context.Context, accountID string, quantity int, unitprice tender.Value) (tender.Value, error) {
	resp, err := gd.client.LLMUseCredits(ctx, &proxyapi.LLMUseCreditsRequest{
		AccountID:  accountID,
		Quantity:   int64(quantity),
		Microcents: unitprice.Microcents(),
	})
	if err != nil {
		return tender.Zero(), err
	}
	return tender.Mint(0, resp.Microcents), nil
}

// protoCreditInfoToGateway converts a [proxyapi.Creditinfo]
// to an [llmgateway.CreditInfo].
func protoCreditInfoToGateway(ciIn *proxyapi.CreditInfo) *llmgateway.CreditInfo {
	var ciOut llmgateway.CreditInfo
	ciOut.Available = ciIn.Available
	ciOut.Max = ciIn.Max
	ciOut.RefreshPerHour = ciIn.RefreshPerHour
	ciOut.LastRefresh = ciIn.LastRefresh.AsTime()
	ciOut.Plan.Name = ciIn.Plan.Name
	ciOut.Plan.MaxCredit = ciIn.Plan.MaxCredit
	ciOut.Plan.RefreshPerHour = ciIn.Plan.RefreshPerHour
	ciOut.Plan.CreditExhaustedError = ciIn.Plan.CreditExhaustedError
	return &ciOut
}
