package money

import (
	"fmt"
	"math/big"
)

// RateScale is the fixed-point scale for FX rates: a rate of 478.25 KZT/USD
// is stored as 478_250_000 micros. Rates are never floats.
const RateScale = 1_000_000

// Convert applies a rate to an amount: result = round(value × rateMicros / 1e6),
// rounded half-to-even (banker's rounding). Used when converting FROM the base
// currency of the pair (e.g. USD→KZT with a USDKZT rate).
func Convert(value, rateMicros int64) (int64, error) {
	return mulDivHalfEven(value, rateMicros, RateScale)
}

// ConvertInverse divides by a rate: result = round(value × 1e6 / rateMicros),
// rounded half-to-even. Used when converting TO the base currency of the pair
// (e.g. KZT→USD with a USDKZT rate).
func ConvertInverse(value, rateMicros int64) (int64, error) {
	if rateMicros == 0 {
		return 0, fmt.Errorf("%w: zero rate", ErrInvalidAmount)
	}
	return mulDivHalfEven(value, RateScale, rateMicros)
}

// FormatRate renders a micro-rate as a decimal string, e.g. 478250000 → "478.250000".
func FormatRate(rateMicros int64) string {
	neg := ""
	if rateMicros < 0 {
		neg = "-"
		rateMicros = -rateMicros
	}
	return fmt.Sprintf("%s%d.%06d", neg, rateMicros/RateScale, rateMicros%RateScale)
}

// mulDivHalfEven computes v×num/den with IEEE 754-style round-half-to-even,
// using big.Int internally so the intermediate product cannot overflow.
func mulDivHalfEven(v, num, den int64) (int64, error) {
	if den == 0 {
		return 0, fmt.Errorf("%w: zero denominator", ErrInvalidAmount)
	}
	n := new(big.Int).Mul(big.NewInt(v), big.NewInt(num))
	d := big.NewInt(den)

	q, r := new(big.Int).QuoRem(n, d, new(big.Int))
	if r.Sign() != 0 {
		// Compare |2r| with |d|; on tie round to even, otherwise to nearest.
		r2 := new(big.Int).Lsh(new(big.Int).Abs(r), 1)
		cmp := r2.Cmp(new(big.Int).Abs(d))
		roundAway := cmp > 0 || (cmp == 0 && q.Bit(0) == 1)
		if roundAway {
			if (n.Sign() < 0) != (d.Sign() < 0) {
				q.Sub(q, big.NewInt(1))
			} else {
				q.Add(q, big.NewInt(1))
			}
		}
	}
	if !q.IsInt64() {
		return 0, ErrOverflow
	}
	return q.Int64(), nil
}
