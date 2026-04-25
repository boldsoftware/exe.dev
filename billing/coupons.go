package billing

import (
	"context"
	"fmt"
	"time"

	"github.com/stripe/stripe-go/v85"
)

// CouponInfo holds display-safe details about a Stripe coupon.
type CouponInfo struct {
	ID               string
	Name             string
	PercentOff       float64
	AmountOffCents   int64
	Currency         string
	Duration         string
	DurationInMonths int64
	MaxRedemptions   int64
	TimesRedeemed    int64
	Valid            bool
	Created          time.Time
	RedeemBy         *time.Time
}

// CouponCustomer holds a Stripe customer that has redeemed a coupon.
type CouponCustomer struct {
	ID             string
	Email          string
	Name           string
	DiscountStart  time.Time
	DiscountEnd    *time.Time
	PromotionCode  string
}

// ListCoupons returns all coupons from Stripe.
func (m *Manager) ListCoupons(ctx context.Context) ([]CouponInfo, error) {
	c := m.client()
	params := &stripe.CouponListParams{}
	params.ListParams.Limit = stripe.Int64(100)

	var result []CouponInfo
	for coupon, err := range c.V1Coupons.List(ctx, params).All(ctx) {
		if err != nil {
			return nil, fmt.Errorf("list coupons: %w", err)
		}
		info := CouponInfo{
			ID:               coupon.ID,
			Name:             coupon.Name,
			PercentOff:       coupon.PercentOff,
			AmountOffCents:   coupon.AmountOff,
			Currency:         string(coupon.Currency),
			Duration:         string(coupon.Duration),
			DurationInMonths: coupon.DurationInMonths,
			MaxRedemptions:   coupon.MaxRedemptions,
			TimesRedeemed:    coupon.TimesRedeemed,
			Valid:            coupon.Valid,
			Created:          time.Unix(coupon.Created, 0).UTC(),
		}
		if coupon.RedeemBy > 0 {
			t := time.Unix(coupon.RedeemBy, 0).UTC()
			info.RedeemBy = &t
		}
		result = append(result, info)
	}
	return result, nil
}

// GetCoupon retrieves a single coupon by ID from Stripe.
func (m *Manager) GetCoupon(ctx context.Context, couponID string) (*CouponInfo, error) {
	c := m.client()
	coupon, err := c.V1Coupons.Retrieve(ctx, couponID, nil)
	if err != nil {
		return nil, fmt.Errorf("retrieve coupon %s: %w", couponID, err)
	}
	info := &CouponInfo{
		ID:               coupon.ID,
		Name:             coupon.Name,
		PercentOff:       coupon.PercentOff,
		AmountOffCents:   coupon.AmountOff,
		Currency:         string(coupon.Currency),
		Duration:         string(coupon.Duration),
		DurationInMonths: coupon.DurationInMonths,
		MaxRedemptions:   coupon.MaxRedemptions,
		TimesRedeemed:    coupon.TimesRedeemed,
		Valid:            coupon.Valid,
		Created:          time.Unix(coupon.Created, 0).UTC(),
	}
	if coupon.RedeemBy > 0 {
		t := time.Unix(coupon.RedeemBy, 0).UTC()
		info.RedeemBy = &t
	}
	return info, nil
}

// ListCouponCustomers returns the Stripe customers that have redeemed a specific coupon.
// It uses the Stripe Customer Search API to find customers with a matching discount coupon.
func (m *Manager) ListCouponCustomers(ctx context.Context, couponID string) ([]CouponCustomer, error) {
	c := m.client()
	params := &stripe.CustomerSearchParams{}
	params.Query = fmt.Sprintf("discount.coupon:'%s'", couponID)
	params.Limit = stripe.Int64(100)
	params.AddExpand("data.discount")
	params.AddExpand("data.discount.promotion_code")

	var result []CouponCustomer
	for customer, err := range c.V1Customers.Search(ctx, params).All(ctx) {
		if err != nil {
			return nil, fmt.Errorf("search customers for coupon %s: %w", couponID, err)
		}
		cc := CouponCustomer{
			ID:    customer.ID,
			Email: customer.Email,
			Name:  customer.Name,
		}
		if customer.Discount != nil {
			cc.DiscountStart = time.Unix(customer.Discount.Start, 0).UTC()
			if customer.Discount.End > 0 {
				t := time.Unix(customer.Discount.End, 0).UTC()
				cc.DiscountEnd = &t
			}
			if customer.Discount.PromotionCode != nil {
				cc.PromotionCode = customer.Discount.PromotionCode.Code
			}
		}
		result = append(result, cc)
	}
	return result, nil
}
