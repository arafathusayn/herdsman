package policy

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Dimension names one limit of a budget.
type Dimension string

// The budget dimensions.
const (
	DimensionWall    Dimension = "wall"
	DimensionMachine Dimension = "machine"
	DimensionTokens  Dimension = "tokens"
	DimensionCost    Dimension = "cost"
)

// Budget holds the limits of one task or route. Zero means no limit.
type Budget struct {
	Wall       time.Duration
	Machine    time.Duration
	Tokens     int64
	CostMicros int64
}

// Usage is what one task or route has consumed so far. A zero Started means the
// start is unknown, so the wall limit cannot be measured.
type Usage struct {
	Started    time.Time
	Machine    time.Duration
	Tokens     int64
	CostMicros int64
}

// Exceeded is one limit that has been reached, in display form.
type Exceeded struct {
	Dimension Dimension
	Limit     string
	Used      string
	Overshoot string
}

// Check lists the limits usage has reached, in a fixed order. Reaching a limit
// exactly counts: a budget of one hour is spent at one hour.
func (b Budget) Check(u Usage, now time.Time) []Exceeded {
	var out []Exceeded
	if b.Wall > 0 && !u.Started.IsZero() {
		if used := now.Sub(u.Started); used >= b.Wall {
			out = append(out, durationExceeded(DimensionWall, b.Wall, used))
		}
	}
	if b.Machine > 0 && u.Machine >= b.Machine {
		out = append(out, durationExceeded(DimensionMachine, b.Machine, u.Machine))
	}
	if b.Tokens > 0 && u.Tokens >= b.Tokens {
		out = append(out, Exceeded{
			Dimension: DimensionTokens,
			Limit:     strconv.FormatInt(b.Tokens, 10),
			Used:      strconv.FormatInt(u.Tokens, 10),
			Overshoot: strconv.FormatInt(u.Tokens-b.Tokens, 10),
		})
	}
	if b.CostMicros > 0 && u.CostMicros >= b.CostMicros {
		out = append(out, Exceeded{
			Dimension: DimensionCost,
			Limit:     costText(b.CostMicros),
			Used:      costText(u.CostMicros),
			Overshoot: costText(u.CostMicros - b.CostMicros),
		})
	}
	return out
}

func durationExceeded(d Dimension, limit, used time.Duration) Exceeded {
	return Exceeded{Dimension: d, Limit: limit.String(), Used: used.String(), Overshoot: (used - limit).String()}
}

// costText prints millionths with at least two decimals and no lost digits.
func costText(micros int64) string {
	text := fmt.Sprintf("%d.%06d", micros/microsPerUnit, micros%microsPerUnit)
	for strings.HasSuffix(text, "0") && len(text)-strings.IndexByte(text, '.') > 3 {
		text = text[:len(text)-1]
	}
	return "$" + text
}
