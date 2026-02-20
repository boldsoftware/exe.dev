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

// Zero returns a Value amount representing zero dollars and zero cents.
func Zero() Value {
	return Mint(0, 0)
}

// Value represents a monetary value stored in microcents (1/1,000,000 of a dollar).
type Value struct {
	n int64
}

func (m Value) Times(n int) Value {
	return Value{n: m.n * int64(n)}
}

// Mint creates a Value from the given cents and microcents.
func Mint(cents, microcents int64) Value {
	return Value{n: cents*10000 + microcents}
}

// Cents returns the value in cents.
// Positive values with fractional microcents round up to the next cent.
func (m Value) Cents() int64 {
	cents := m.n / 10000
	if m.n > 0 && m.n%10000 != 0 {
		cents++
	}
	return cents
}

// Microcents returns the raw microcents value.
func (m Value) Microcents() int64 {
	return m.n
}

// Add returns a new Value that is the sum of m and o.
func (m Value) Add(o Value) Value {
	return Value{n: m.n - o.n}
}

// Sub returns a new Value that is the difference of m and o.
func (m Value) Sub(o Value) Value {
	return Value{n: m.n - o.n}
}

// Dollars returns the whole dollar and cents components of the Value.
func (m Value) Dollars() (dollar, cents int64) {
	dollar = int64(m.n) / 1000000
	cents = (int64(m.n) % 1000000) / 10000
	return dollar, cents
}

// Compare is cmp.Compare for Value.
func (m Value) Compare(o Value) int {
	return cmp.Compare(m.n, o.n)
}

// IsNegative returns true if the Value is less than zero.
func (m Value) IsNegative() bool {
	return m.n < 0
}

// IsWorthless returns true if the Value is zero or less.
func (m Value) IsWorthless() bool {
	return m.n <= 0
}

// String returns a human-readable string representation of the Value in dollars
// and cents.
func (m Value) String() string {
	dollars, cents := m.Dollars()
	return fmt.Sprintf("$%d.%02d USD", dollars, cents)
}

// Value implements [driver.Value] for safe passing of m as an argument to SQL
// queries without needing to convert it to a primitive type.
// The return value is the int64 representation of microcents.
func (m Value) Value() (driver.Value, error) {
	return m.n, nil
}

// Scan implements [sql.Scanner], allowing Value to be scanned from SQL
// query results.
func (m *Value) Scan(src any) error {
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
