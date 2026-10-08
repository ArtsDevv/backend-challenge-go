package money_test

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"backend-challenge-go/internal/domain/money"
)

func TestNewMoneyFromString_Valid(t *testing.T) {
	tests := []struct {
		name       string
		amount     string
		currency   string
		wantMinor  int64
		wantString string
	}{
		{"typical amount", "25.00", "BRL", 2500, "25.00"},
		{"zero is accepted", "0.00", "BRL", 0, "0.00"},
		{"bare zero", "0", "BRL", 0, "0.00"},
		{"integer only gets padded", "25", "BRL", 2500, "25.00"},
		{"single decimal digit gets padded", "25.5", "BRL", 2550, "25.50"},
		{"large amount", "1000000.00", "BRL", 100000000, "1000000.00"},
		{"currency is normalized to uppercase", "10.00", "brl", 1000, "10.00"},
		{"currency with surrounding whitespace", "10.00", " BRL ", 1000, "10.00"},
		{"amount with surrounding whitespace", " 10.00 ", "BRL", 1000, "10.00"},
		{"leading zeros in integer part", "007.50", "BRL", 750, "7.50"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := money.NewMoneyFromString(tt.amount, tt.currency)
			require.NoError(t, err)
			assert.Equal(t, tt.wantMinor, m.MinorUnits())
			assert.Equal(t, "BRL", m.Currency())
			assert.Equal(t, tt.wantString+" BRL", m.String())
		})
	}
}

func TestNewMoneyFromString_Invalid(t *testing.T) {
	tests := []struct {
		name     string
		amount   string
		currency string
		wantErr  error
	}{
		{"empty amount", "", "BRL", money.ErrEmptyAmount},
		{"whitespace only amount", "   ", "BRL", money.ErrEmptyAmount},
		{"NaN is rejected", "NaN", "BRL", money.ErrInvalidFormat},
		{"Infinity is rejected", "Infinity", "BRL", money.ErrInvalidFormat},
		{"negative infinity is rejected", "-Infinity", "BRL", money.ErrInvalidFormat},
		{"scientific notation is rejected", "1e10", "BRL", money.ErrInvalidFormat},
		{"garbage text is rejected", "abc", "BRL", money.ErrInvalidFormat},
		{"trailing dot with no digits", "25.", "BRL", money.ErrInvalidFormat},
		{"leading dot with no integer part", ".50", "BRL", money.ErrInvalidFormat},
		{"excess scale is rejected", "25.123", "BRL", money.ErrScaleExceeded},
		{"negative amount is rejected", "-25.00", "BRL", money.ErrNegativeNotAllowed},
		{"negative zero is rejected", "-0.01", "BRL", money.ErrNegativeNotAllowed},
		{"empty currency", "25.00", "", money.ErrInvalidCurrency},
		{"too-short currency", "25.00", "BR", money.ErrInvalidCurrency},
		{"too-long currency", "25.00", "BRLL", money.ErrInvalidCurrency},
		{"numeric currency", "25.00", "123", money.ErrInvalidCurrency},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := money.NewMoneyFromString(tt.amount, tt.currency)
			require.Error(t, err)
			assert.True(t, errors.Is(err, tt.wantErr), "expected error to be %v, got %v", tt.wantErr, err)
		})
	}
}

func TestNewMoneyFromString_OverflowOnParse(t *testing.T) {
	_, err := money.NewMoneyFromString("99999999999999999999999.00", "BRL")
	require.Error(t, err)
	assert.True(t, errors.Is(err, money.ErrOverflow))
}

func TestZero(t *testing.T) {
	z, err := money.Zero("BRL")
	require.NoError(t, err)
	assert.True(t, z.IsZero())
	assert.False(t, z.IsPositive())
	assert.False(t, z.IsNegative())
	assert.Equal(t, int64(0), z.MinorUnits())
	assert.Equal(t, "BRL", z.Currency())

	_, err = money.Zero("")
	assert.True(t, errors.Is(err, money.ErrInvalidCurrency))
}

func TestFromMinorUnits_AllowsNegative(t *testing.T) {
	m, err := money.FromMinorUnits(-1500, "BRL")
	require.NoError(t, err)
	assert.True(t, m.IsNegative())
	assert.Equal(t, int64(-1500), m.MinorUnits())
	assert.Equal(t, "-15.00 BRL", m.String())

	_, err = money.FromMinorUnits(100, "xyz")
	require.NoError(t, err) // alphabetic 3-letter code is accepted regardless of case
}

func TestAdd(t *testing.T) {
	a, err := money.NewMoneyFromString("25.00", "BRL")
	require.NoError(t, err)
	b, err := money.NewMoneyFromString("15.50", "BRL")
	require.NoError(t, err)

	sum, err := a.Add(b)
	require.NoError(t, err)
	assert.Equal(t, int64(4050), sum.MinorUnits())
	assert.Equal(t, "BRL", sum.Currency())
}

func TestAdd_CurrencyMismatch(t *testing.T) {
	brl, err := money.NewMoneyFromString("25.00", "BRL")
	require.NoError(t, err)
	usd, err := money.NewMoneyFromString("25.00", "USD")
	require.NoError(t, err)

	_, err = brl.Add(usd)
	require.Error(t, err)
	assert.True(t, errors.Is(err, money.ErrCurrencyMismatch))
}

func TestAdd_Overflow(t *testing.T) {
	a, err := money.FromMinorUnits(math.MaxInt64, "BRL")
	require.NoError(t, err)
	b, err := money.FromMinorUnits(1, "BRL")
	require.NoError(t, err)

	_, err = a.Add(b)
	require.Error(t, err)
	assert.True(t, errors.Is(err, money.ErrOverflow))
}

func TestSub(t *testing.T) {
	a, err := money.NewMoneyFromString("25.00", "BRL")
	require.NoError(t, err)
	b, err := money.NewMoneyFromString("15.50", "BRL")
	require.NoError(t, err)

	diff, err := a.Sub(b)
	require.NoError(t, err)
	assert.Equal(t, int64(950), diff.MinorUnits())
}

func TestSub_CanProduceNegative(t *testing.T) {
	a, err := money.NewMoneyFromString("10.00", "BRL")
	require.NoError(t, err)
	b, err := money.NewMoneyFromString("25.00", "BRL")
	require.NoError(t, err)

	diff, err := a.Sub(b)
	require.NoError(t, err)
	assert.True(t, diff.IsNegative())
	assert.Equal(t, int64(-1500), diff.MinorUnits())
}

func TestSub_CurrencyMismatch(t *testing.T) {
	brl, err := money.NewMoneyFromString("25.00", "BRL")
	require.NoError(t, err)
	usd, err := money.NewMoneyFromString("25.00", "USD")
	require.NoError(t, err)

	_, err = brl.Sub(usd)
	require.Error(t, err)
	assert.True(t, errors.Is(err, money.ErrCurrencyMismatch))
}

func TestSub_Overflow(t *testing.T) {
	a, err := money.FromMinorUnits(math.MinInt64, "BRL")
	require.NoError(t, err)
	b, err := money.FromMinorUnits(1, "BRL")
	require.NoError(t, err)

	_, err = a.Sub(b)
	require.Error(t, err)
	assert.True(t, errors.Is(err, money.ErrOverflow))
}

func TestNegate(t *testing.T) {
	a, err := money.NewMoneyFromString("25.00", "BRL")
	require.NoError(t, err)

	negated, err := a.Negate()
	require.NoError(t, err)
	assert.Equal(t, int64(-2500), negated.MinorUnits())

	backToPositive, err := negated.Negate()
	require.NoError(t, err)
	assert.True(t, backToPositive.Equal(a))
}

func TestNegate_MinInt64Overflows(t *testing.T) {
	m, err := money.FromMinorUnits(math.MinInt64, "BRL")
	require.NoError(t, err)

	_, err = m.Negate()
	require.Error(t, err)
	assert.True(t, errors.Is(err, money.ErrOverflow))
}

func TestCompare(t *testing.T) {
	small, err := money.NewMoneyFromString("10.00", "BRL")
	require.NoError(t, err)
	big, err := money.NewMoneyFromString("20.00", "BRL")
	require.NoError(t, err)
	sameAsSmall, err := money.NewMoneyFromString("10.00", "BRL")
	require.NoError(t, err)

	cmp, err := small.Compare(big)
	require.NoError(t, err)
	assert.Equal(t, -1, cmp)

	cmp, err = big.Compare(small)
	require.NoError(t, err)
	assert.Equal(t, 1, cmp)

	cmp, err = small.Compare(sameAsSmall)
	require.NoError(t, err)
	assert.Equal(t, 0, cmp)
}

func TestCompare_CurrencyMismatch(t *testing.T) {
	brl, err := money.NewMoneyFromString("10.00", "BRL")
	require.NoError(t, err)
	usd, err := money.NewMoneyFromString("10.00", "USD")
	require.NoError(t, err)

	_, err = brl.Compare(usd)
	require.Error(t, err)
	assert.True(t, errors.Is(err, money.ErrCurrencyMismatch))
}

func TestEqual(t *testing.T) {
	a, err := money.NewMoneyFromString("10.00", "BRL")
	require.NoError(t, err)
	b, err := money.NewMoneyFromString("10.00", "BRL")
	require.NoError(t, err)
	c, err := money.NewMoneyFromString("10.00", "USD")
	require.NoError(t, err)
	d, err := money.NewMoneyFromString("10.01", "BRL")
	require.NoError(t, err)

	assert.True(t, a.Equal(b))
	assert.False(t, a.Equal(c), "different currency must never be equal")
	assert.False(t, a.Equal(d))
}

func TestMarshalJSON(t *testing.T) {
	m, err := money.NewMoneyFromString("25.00", "BRL")
	require.NoError(t, err)

	data, err := json.Marshal(m)
	require.NoError(t, err)
	assert.JSONEq(t, `{"amount":"25.00","currency":"BRL"}`, string(data))
}

func TestMarshalJSON_InStruct(t *testing.T) {
	type payload struct {
		Money money.Money `json:"money"`
	}
	m, err := money.NewMoneyFromString("5.00", "BRL")
	require.NoError(t, err)

	data, err := json.Marshal(payload{Money: m})
	require.NoError(t, err)
	assert.JSONEq(t, `{"money":{"amount":"5.00","currency":"BRL"}}`, string(data))
}

func TestUnmarshalJSON_Valid(t *testing.T) {
	var m money.Money
	err := json.Unmarshal([]byte(`{"amount":"25.00","currency":"BRL"}`), &m)
	require.NoError(t, err)
	assert.Equal(t, int64(2500), m.MinorUnits())
	assert.Equal(t, "BRL", m.Currency())
}

func TestUnmarshalJSON_RejectsExternalInvariantsTooJustLikeParsing(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		wantErr error
	}{
		{"negative amount", `{"amount":"-10.00","currency":"BRL"}`, money.ErrNegativeNotAllowed},
		{"excess scale", `{"amount":"10.123","currency":"BRL"}`, money.ErrScaleExceeded},
		{"empty amount", `{"amount":"","currency":"BRL"}`, money.ErrEmptyAmount},
		{"NaN amount", `{"amount":"NaN","currency":"BRL"}`, money.ErrInvalidFormat},
		{"invalid currency", `{"amount":"10.00","currency":"X"}`, money.ErrInvalidCurrency},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m money.Money
			err := json.Unmarshal([]byte(tt.payload), &m)
			require.Error(t, err)
			assert.True(t, errors.Is(err, tt.wantErr))
		})
	}
}

func TestJSONRoundTrip(t *testing.T) {
	original, err := money.NewMoneyFromString("1234.56", "BRL")
	require.NoError(t, err)

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded money.Money
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.True(t, original.Equal(decoded))
}
