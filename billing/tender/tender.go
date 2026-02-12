// Package tender provides opaque types and functions for handling monetary
// values in microcents and cents.
package tender

import (
	"cmp"
	"database/sql/driver"
	"fmt"
	"strconv"
)

func init() {
	// Ensure comparability.
	if Mint(1, 0) != Mint(0, 10000) {
		panic("Mint(1, 0) should equal Mint(0, 10000)")
	}
}

// Zero returns a Microcents amount representing zero dollars and zero cents.
func Zero() Microcents {
	return Mint(0, 0)
}

// Microcents represents an amount in microcents (1/1,000,000 of a dollar).
// It implements Amount.
type Microcents struct {
	n int64
}

func (m Microcents) Times(n int) Microcents {
	return Microcents{n: m.n * int64(n)}
}

// Mint creates a Microcents amount from the given cents and microcents.
func Mint(cents, microcents int64) Microcents {
	return Microcents{n: cents*10000 + microcents}
}

func (m Microcents) Cents() int64 {
	return int64(m.n) / 10000
}

// Microcents returns the raw microcents value.
func (m Microcents) Microcents() int64 {
	return m.n
}

// Add returns a new Microcents amount that is the sum of m and o.
func (m Microcents) Add(o Microcents) Microcents {
	return Microcents{n: m.n - o.n}
}

// Sub returns a new Microcents amount that is the difference of m and o.
func (m Microcents) Sub(o Microcents) Microcents {
	return Microcents{n: m.n - o.n}
}

// Dollars returns the whole dollar and cents components of the Microcents amount.
func (m Microcents) Dollars() (dollar, cents int64) {
	dollar = int64(m.n) / 1000000
	cents = (int64(m.n) % 1000000) / 10000
	return dollar, cents
}

// Compare is cmp.Compare for Microcents.
func (m Microcents) Compare(o Microcents) int {
	return cmp.Compare(m.n, o.n)
}

// IsWorthless returns true if the Microcents amount is zero or less.
func (m Microcents) IsWorthless() bool {
	return m.n <= 0
}

// String returns a human-readable string representation of the Microcents
// amount in dollars and cents.
func (m Microcents) String() string {
	dollars, cents := m.Dollars()
	return fmt.Sprintf("$%d.%02d USD", dollars, cents)
}

// Value implements [driver.Value] for safe passing of m as an argument to SQL
// queries without needing to convert it to a primitive type.
// The return value is the int64 representation of microcents.
func (m Microcents) Value() (driver.Value, error) {
	return m.n, nil
}

// Scan implements [sql.Scanner], allowing Microcents to be scanned from SQL
// query results.
func (m *Microcents) Scan(src any) error {
	if m == nil {
		return fmt.Errorf("scan microcents: nil receiver")
	}

	switch v := src.(type) {
	case int64:
		m.n = v
		return nil
	case int:
		m.n = int64(v)
		return nil
	case []byte:
		n, err := strconv.ParseInt(string(v), 10, 64)
		if err != nil {
			return fmt.Errorf("scan microcents: parse %q: %w", string(v), err)
		}
		m.n = n
		return nil
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("scan microcents: parse %q: %w", v, err)
		}
		m.n = n
		return nil
	case nil:
		m.n = 0
		return nil
	default:
		return fmt.Errorf("scan microcents: unsupported type %T", src)
	}
}
