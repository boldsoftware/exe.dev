package billing

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"exe.dev/billing/stripetest"
	"exe.dev/tslog"
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
	case r.Method == http.MethodGet && (r.URL.Path == "/v1/products/prod_individual" || r.URL.Path == "/v1/products/prod_team"):
		productID := r.URL.Path[len("/v1/products/"):]
		c.mu.Lock()
		_, ok := c.products[productID]
		c.mu.Unlock()

		if !ok {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"resource_missing","message":"No such product"}}`)
			return
		}
		name := "Individual"
		if productID == "prod_team" {
			name = "Team"
		}
		io.WriteString(w, `{"id":"`+productID+`","object":"product","name":"`+name+`"}`)
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
			// Determine product from lookup key
			product := "prod_individual"
			if len(lookupKey) >= 4 && lookupKey[:4] == "team" {
				product = "prod_team"
			}
			io.WriteString(w, fmt.Sprintf(`{"object":"list","data":[{"id":"%s","object":"price","lookup_key":"%s","product":"%s"}],"has_more":false,"url":"/v1/prices"}`,
				priceID, lookupKey, product,
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
		usageType := r.Form.Get("recurring[usage_type]")

		priceID := "price_" + lookupKey

		c.mu.Lock()
		c.prices[lookupKey] = priceID
		c.priceCreates++
		c.lastPriceLookupKey = lookupKey
		c.lastPriceCurrency = currency
		c.lastPriceProductID = product
		c.lastPriceUnitAmount = unitAmount
		if usageType != "" {
			c.lastPriceInterval = "" // metered pricing has no interval
		} else {
			c.lastPriceInterval = interval
		}
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
	m := &Manager{
		Client: stripetest.Client(t, catalog.handle),
		Logger: tslog.Slogger(t),
	}

	if err := m.InstallPrices(t.Context()); err != nil {
		t.Fatalf("InstallPrices: %v", err)
	}

	catalog.mu.Lock()
	defer catalog.mu.Unlock()

	if catalog.productCreates != 2 {
		t.Fatalf("product creates = %d, want 2", catalog.productCreates)
	}
	if catalog.priceCreates != 8 {
		t.Fatalf("price creates = %d, want 8", catalog.priceCreates)
	}
	if catalog.lastProductCreateID != "prod_team" {
		t.Fatalf("last product id = %q, want %q", catalog.lastProductCreateID, "prod_team")
	}
	if catalog.lastProductCreateName != "Team" {
		t.Fatalf("last product name = %q, want %q", catalog.lastProductCreateName, "Team")
	}
	if catalog.lastPriceLookupKey != "team:usage-bandwidth:20260106" {
		t.Fatalf("last price lookup key = %q, want %q", catalog.lastPriceLookupKey, "team:usage-bandwidth:20260106")
	}
	if catalog.lastPriceCurrency != "usd" {
		t.Fatalf("last price currency = %q, want %q", catalog.lastPriceCurrency, "usd")
	}
	if catalog.lastPriceProductID != "prod_team" {
		t.Fatalf("last price product id = %q, want %q", catalog.lastPriceProductID, "prod_team")
	}
	if catalog.lastPriceUnitAmount != 7 {
		t.Fatalf("last price unit amount = %d, want 7", catalog.lastPriceUnitAmount)
	}
}

func TestInstallPricesIsIdempotent(t *testing.T) {
	catalog := newFakeStripeCatalog()
	m := &Manager{
		Client: stripetest.Client(t, catalog.handle),
		Logger: tslog.Slogger(t),
	}

	if err := m.InstallPrices(t.Context()); err != nil {
		t.Fatalf("InstallPrices first call: %v", err)
	}
	if err := m.InstallPrices(t.Context()); err != nil {
		t.Fatalf("InstallPrices second call: %v", err)
	}

	catalog.mu.Lock()
	defer catalog.mu.Unlock()

	if catalog.productCreates != 2 {
		t.Fatalf("product creates = %d, want 2", catalog.productCreates)
	}
	if catalog.priceCreates != 8 {
		t.Fatalf("price creates = %d, want 8", catalog.priceCreates)
	}
}
