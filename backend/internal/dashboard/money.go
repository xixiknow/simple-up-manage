package dashboard

import (
	"encoding/json"
	"math"
	"strconv"
)

// Amount is USD stored as integer micros of a cent of a cent: 1 = $0.00000001.
type Amount int64

const amountScale = 1e8

func AmountFromFloat(v float64) Amount {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return Amount(math.Round(v * amountScale))
}

func (a Amount) Float() float64 {
	return float64(a) / amountScale
}

func (a Amount) Ptr() *float64 {
	v := a.Float()
	return &v
}

func (a Amount) Add(b Amount) Amount { return a + b }
func (a Amount) Sub(b Amount) Amount { return a - b }

func (a Amount) MulFloat(f float64) Amount {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return Amount(math.Round(float64(a) * f))
}

func (a Amount) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatFloat(a.Float(), 'f', 8, 64)), nil
}

func (a *Amount) UnmarshalJSON(b []byte) error {
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	*a = AmountFromFloat(f)
	return nil
}

func round8(v float64) float64 {
	return math.Round(v*amountScale) / amountScale
}

func ptrFloat(v float64) *float64 { return &v }

func cloneFloat(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
