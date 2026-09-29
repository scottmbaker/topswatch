package metricdef

import "strconv"

func formatFloat(v float64, prec int) string {
	return strconv.FormatFloat(v, 'f', prec, 64)
}
