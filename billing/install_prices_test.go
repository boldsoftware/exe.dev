package billing

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"exe.dev/billing/stripetest"
)

type fakeStripeCatalog struct {
	mu sync.Mutex

	products map[string]bool
	prices   map[string]string

	productCreates int
	priceCreates   int

	lastProductCreateID   string
	lastProductCreateName string
	lastPriceLookupKey    string
	lastPriceCurrency     string
	lastPriceProductID    string
	lastPriceUnitAmount   int64
	lastPriceInterval     string
}

func newFakeStripeCatalog() *fakeStripeCatalog {
	return &fakeStripeCatalog{
		products: make(map[string]bool),
		prices:   make(map[string]string),
	}
}

func (c *fakeStripeCatalog) handle(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/products/prod_individual":
		c.mu.Lock()
		_, ok := c.products["prod_individual"]
		c.mu.Unlock()

		if !ok {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"resource_missing","message":"No such product"}}`)
			return
		}
		io.WriteString(w, `{"id":"prod_individual","object":"product","name":"Individual"}`)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/products":
		id := r.Form.Get("id")
		name := r.Form.Get("name")

		c.mu.Lock()
		c.products[id] = true
		c.productCreates++
		c.lastProductCreateID = id
		c.lastProductCreateName = name
		c.mu.Unlock()

		io.WriteString(w, `{"id":"`+id+`","object":"product","name":"`+name+`"}`)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/prices":
		lookupKey := r.Form.Get("lookup_keys[0]")
		c.mu.Lock()
		priceID, ok := c.prices[lookupKey]
		c.mu.Unlock()

		if ok {
			io.WriteString(w, fmt.Sprintf(`{"object":"list","data":[{"id":"%s","object":"price","lookup_key":"%s","product":"prod_individual"}],"has_more":false,"url":"/v1/prices"}`,
				priceID, lookupKey,
			))
			return
		}
		io.WriteString(w, `{"object":"list","data":[],"has_more":false,"url":"/v1/prices"}`)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/prices":
		lookupKey := r.Form.Get("lookup_key")
		currency := r.Form.Get("currency")
		product := r.Form.Get("product")
		unitAmount, _ := strconv.ParseInt(r.Form.Get("unit_amount"), 10, 64)
		interval := r.Form.Get("recurring[interval]")

		priceID := "price_" + lookupKey

		c.mu.Lock()
		c.prices[lookupKey] = priceID
		c.priceCreates++
		c.lastPriceLookupKey = lookupKey
		c.lastPriceCurrency = currency
		c.lastPriceProductID = product
		c.lastPriceUnitAmount = unitAmount
		c.lastPriceInterval = interval
		c.mu.Unlock()

		io.WriteString(w, fmt.Sprintf(`{"id":"%s","object":"price","lookup_key":"%s","product":"%s"}`,
			priceID, lookupKey, product,
		))
	default:
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"resource_missing","message":"not found"}}`)
	}
}

func TestInstallPricesCreatesManagedCatalog(t *testing.T) {
	catalog := newFakeStripeCatalog()
	m := &Manager{Client: stripetest.Client(catalog.handle)}

	if err := m.InstallPrices(t.Context()); err != nil {
		t.Fatalf("InstallPrices: %v", err)
	}

	catalog.mu.Lock()
	defer catalog.mu.Unlock()

	if catalog.productCreates != 1 {
		t.Fatalf("product creates = %d, want 1", catalog.productCreates)
	}
	if catalog.priceCreates != 1 {
		t.Fatalf("price creates = %d, want 1", catalog.priceCreates)
	}
	if catalog.lastProductCreateID != "prod_individual" {
		t.Fatalf("product id = %q, want %q", catalog.lastProductCreateID, "prod_individual")
	}
	if catalog.lastProductCreateName != "Individual" {
		t.Fatalf("product name = %q, want %q", catalog.lastProductCreateName, "Individual")
	}
	if catalog.lastPriceLookupKey != DefaultPlan {
		t.Fatalf("price lookup key = %q, want %q", catalog.lastPriceLookupKey, DefaultPlan)
	}
	if catalog.lastPriceCurrency != "usd" {
		t.Fatalf("price currency = %q, want %q", catalog.lastPriceCurrency, "usd")
	}
	if catalog.lastPriceProductID != "prod_individual" {
		t.Fatalf("price product id = %q, want %q", catalog.lastPriceProductID, "prod_individual")
	}
	if catalog.lastPriceUnitAmount != 2000 {
		t.Fatalf("price unit amount = %d, want 2000", catalog.lastPriceUnitAmount)
	}
	if catalog.lastPriceInterval != "month" {
		t.Fatalf("price interval = %q, want %q", catalog.lastPriceInterval, "month")
	}
}

func TestInstallPricesIsIdempotent(t *testing.T) {
	catalog := newFakeStripeCatalog()
	m := &Manager{Client: stripetest.Client(catalog.handle)}

	if err := m.InstallPrices(t.Context()); err != nil {
		t.Fatalf("InstallPrices first call: %v", err)
	}
	if err := m.InstallPrices(t.Context()); err != nil {
		t.Fatalf("InstallPrices second call: %v", err)
	}

	catalog.mu.Lock()
	defer catalog.mu.Unlock()

	if catalog.productCreates != 1 {
		t.Fatalf("product creates = %d, want 1", catalog.productCreates)
	}
	if catalog.priceCreates != 1 {
		t.Fatalf("price creates = %d, want 1", catalog.priceCreates)
	}
}
