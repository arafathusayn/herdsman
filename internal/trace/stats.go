package trace

import "math"

// Interval is a 95% confidence interval.
type Interval struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}

// studentTTable holds the two-sided 95% critical values by degrees of freedom.
var studentTTable = []float64{
	12.706, 4.303, 3.182, 2.776, 2.571, 2.447, 2.365, 2.306, 2.262, 2.228,
	2.201, 2.179, 2.160, 2.145, 2.131, 2.120, 2.110, 2.101, 2.093, 2.086,
	2.080, 2.074, 2.069, 2.064, 2.060, 2.056, 2.052, 2.048, 2.045, 2.042,
}

// z95 is the two-sided 95% critical value of the normal distribution.
const z95 = 1.959963984540054

// studentT rounds down to the nearest tabulated degrees of freedom past the
// exact table. That value is the larger one, so an interval is never narrower
// than 95% promises. These are the benchmark gate's values.
func studentT(df int) float64 {
	switch {
	case df <= len(studentTTable):
		return studentTTable[df-1]
	case df < 40:
		return studentTTable[len(studentTTable)-1]
	case df < 60:
		return 2.0211
	case df < 120:
		return 2.0003
	}
	return 1.980
}

func sampleMean(samples []float64) float64 {
	var sum float64
	for _, s := range samples {
		sum += s
	}
	return sum / float64(len(samples))
}

// varianceOfMean is the squared standard error of the sample mean.
func varianceOfMean(samples []float64, mean float64) float64 {
	var squares float64
	for _, s := range samples {
		squares += (s - mean) * (s - mean)
	}
	n := float64(len(samples))
	return squares / (n - 1) / n
}

// meanInterval is the 95% interval of the mean; it needs two samples.
func meanInterval(samples []float64) (Interval, bool) {
	if len(samples) < 2 {
		return Interval{}, false
	}
	mean := sampleMean(samples)
	half := studentT(len(samples)-1) * math.Sqrt(varianceOfMean(samples, mean))
	return Interval{Low: mean - half, High: mean + half}, true
}

// meanDifference is the 95% interval of mean(a) - mean(b), by Welch's method.
// Its degrees of freedom are rounded down, which widens the interval.
func meanDifference(a, b []float64) (Interval, bool) {
	if len(a) < 2 || len(b) < 2 {
		return Interval{}, false
	}
	meanA, meanB := sampleMean(a), sampleMean(b)
	varA, varB := varianceOfMean(a, meanA), varianceOfMean(b, meanB)
	difference := meanA - meanB
	if varA+varB == 0 {
		return Interval{Low: difference, High: difference}, true
	}
	df := (varA + varB) * (varA + varB) / (varA*varA/float64(len(a)-1) + varB*varB/float64(len(b)-1))
	half := studentT(int(math.Floor(df))) * math.Sqrt(varA+varB)
	return Interval{Low: difference - half, High: difference + half}, true
}

// wilson is the 95% score interval of a proportion, which stays inside 0..1
// and behaves at 0 and at n, where the textbook normal interval collapses.
func wilson(successes, n int) (Interval, bool) {
	if n == 0 {
		return Interval{}, false
	}
	total, rate := float64(n), float64(successes)/float64(n)
	z2 := z95 * z95
	denominator := 1 + z2/total
	center := (rate + z2/(2*total)) / denominator
	half := z95 * math.Sqrt(rate*(1-rate)/total+z2/(4*total*total)) / denominator
	low, high := math.Max(0, center-half), math.Min(1, center+half)
	if successes == 0 {
		low = 0
	}
	if successes == n {
		high = 1
	}
	return Interval{Low: low, High: high}, true
}

// rateDifference is the 95% interval of rate(a) - rate(b), by Newcombe's
// method on the two Wilson intervals.
func rateDifference(successesA, nA, successesB, nB int) (Interval, bool) {
	a, okA := wilson(successesA, nA)
	b, okB := wilson(successesB, nB)
	if !okA || !okB {
		return Interval{}, false
	}
	rateA, rateB := float64(successesA)/float64(nA), float64(successesB)/float64(nB)
	difference := rateA - rateB
	return Interval{
		Low:  difference - math.Hypot(rateA-a.Low, b.High-rateB),
		High: difference + math.Hypot(a.High-rateA, rateB-b.Low),
	}, true
}
