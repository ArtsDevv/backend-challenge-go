package money

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var (
	ErrEmptyAmount        = errors.New("money: amount is empty")
	ErrInvalidFormat      = errors.New("money: amount has an invalid format")
	ErrScaleExceeded      = errors.New("money: amount has more than 2 decimal places")
	ErrNegativeNotAllowed = errors.New("money: negative amount is not allowed as external input")
	ErrInvalidCurrency    = errors.New("money: currency is not a valid ISO 4217 code")
	ErrCurrencyMismatch   = errors.New("money: currency mismatch between operands")
	ErrOverflow           = errors.New("money: arithmetic overflow")
)

type Money struct {
	amountMinor int64
	currency    string
}

var (
	currencyPattern = regexp.MustCompile(`^[A-Za-z]{3}$`)
	decimalPattern  = regexp.MustCompile(`^(-?)(\d+)\.(\d+)$`)
	integerPattern  = regexp.MustCompile(`^(-?)(\d+)$`)
)

func normalizeCurrency(currency string) (string, error) {
	trimmed := strings.TrimSpace(currency)
	if !currencyPattern.MatchString(trimmed) {
		return "", ErrInvalidCurrency
	}
	return strings.ToUpper(trimmed), nil
}

func Zero(currency string) (Money, error) {
	cur, err := normalizeCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	return Money{amountMinor: 0, currency: cur}, nil
}

func FromMinorUnits(amountMinor int64, currency string) (Money, error) {
	cur, err := normalizeCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	return Money{amountMinor: amountMinor, currency: cur}, nil
}

func NewMoneyFromString(amount, currency string) (Money, error) {
	cur, err := normalizeCurrency(currency)
	if err != nil {
		return Money{}, err
	}

	trimmed := strings.TrimSpace(amount)
	if trimmed == "" {
		return Money{}, ErrEmptyAmount
	}

	negative, minor, err := parseDecimalMinorUnits(trimmed)
	if err != nil {
		return Money{}, err
	}
	if negative {
		return Money{}, ErrNegativeNotAllowed
	}

	return Money{amountMinor: minor, currency: cur}, nil
}

func parseDecimalMinorUnits(s string) (negative bool, minorUnits int64, err error) {
	if m := decimalPattern.FindStringSubmatch(s); m != nil {
		sign, intPart, fracPart := m[1], m[2], m[3]
		if len(fracPart) > 2 {
			return false, 0, ErrScaleExceeded
		}
		for len(fracPart) < 2 {
			fracPart += "0"
		}
		return buildMinorUnits(sign, intPart, fracPart)
	}
	if m := integerPattern.FindStringSubmatch(s); m != nil {
		sign, intPart := m[1], m[2]
		return buildMinorUnits(sign, intPart, "00")
	}
	return false, 0, ErrInvalidFormat
}

func buildMinorUnits(sign, intPart, fracPart string) (negative bool, minorUnits int64, err error) {
	negative = sign == "-"

	intVal, convErr := strconv.ParseInt(intPart, 10, 64)
	if convErr != nil {
		// intPart matched \d+, so the only possible failure is out-of-range.
		return false, 0, fmt.Errorf("%w: integer part out of range", ErrOverflow)
	}
	fracVal, convErr := strconv.ParseInt(fracPart, 10, 64)
	if convErr != nil {
		return false, 0, ErrInvalidFormat
	}

	if intVal > (math.MaxInt64-fracVal)/100 {
		return false, 0, ErrOverflow
	}
	minor := intVal*100 + fracVal

	if negative {
		minor = -minor
	}
	return negative, minor, nil
}

func (m Money) MinorUnits() int64 { return m.amountMinor }

func (m Money) Currency() string { return m.currency }

func (m Money) IsZero() bool { return m.amountMinor == 0 }

func (m Money) IsPositive() bool { return m.amountMinor > 0 }

func (m Money) IsNegative() bool { return m.amountMinor < 0 }

func (m Money) sameCurrency(other Money) error {
	if m.currency != other.currency {
		return fmt.Errorf("%w: %s vs %s", ErrCurrencyMismatch, m.currency, other.currency)
	}
	return nil
}

func (m Money) Add(other Money) (Money, error) {
	if err := m.sameCurrency(other); err != nil {
		return Money{}, err
	}
	sum, ok := addInt64(m.amountMinor, other.amountMinor)
	if !ok {
		return Money{}, ErrOverflow
	}
	return Money{amountMinor: sum, currency: m.currency}, nil
}

func (m Money) Sub(other Money) (Money, error) {
	if err := m.sameCurrency(other); err != nil {
		return Money{}, err
	}
	diff, ok := subInt64(m.amountMinor, other.amountMinor)
	if !ok {
		return Money{}, ErrOverflow
	}
	return Money{amountMinor: diff, currency: m.currency}, nil
}

func (m Money) Negate() (Money, error) {
	if m.amountMinor == math.MinInt64 {
		return Money{}, ErrOverflow
	}
	return Money{amountMinor: -m.amountMinor, currency: m.currency}, nil
}

func (m Money) Compare(other Money) (int, error) {
	if err := m.sameCurrency(other); err != nil {
		return 0, err
	}
	switch {
	case m.amountMinor < other.amountMinor:
		return -1, nil
	case m.amountMinor > other.amountMinor:
		return 1, nil
	default:
		return 0, nil
	}
}

func (m Money) Equal(other Money) bool {
	return m.currency == other.currency && m.amountMinor == other.amountMinor
}

func (m Money) decimalString() string {
	abs := m.amountMinor
	negative := abs < 0
	if negative {
		abs = -abs
	}
	whole, cents := abs/100, abs%100
	if negative {
		return fmt.Sprintf("-%d.%02d", whole, cents)
	}
	return fmt.Sprintf("%d.%02d", whole, cents)
}

func (m Money) String() string {
	return fmt.Sprintf("%s %s", m.decimalString(), m.currency)
}

type jsonMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func (m Money) MarshalJSON() ([]byte, error) {
	return json.Marshal(jsonMoney{Amount: m.decimalString(), Currency: m.currency})
}

func (m *Money) UnmarshalJSON(data []byte) error {
	var raw jsonMoney
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidFormat, err)
	}
	parsed, err := NewMoneyFromString(raw.Amount, raw.Currency)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

func addInt64(a, b int64) (sum int64, ok bool) {
	sum = a + b
	if (b > 0 && sum < a) || (b < 0 && sum > a) {
		return 0, false
	}
	return sum, true
}

func subInt64(a, b int64) (diff int64, ok bool) {
	diff = a - b
	if (b < 0 && diff < a) || (b > 0 && diff > a) {
		return 0, false
	}
	return diff, true
}
