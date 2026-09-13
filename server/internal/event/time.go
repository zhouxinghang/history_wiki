package event

import (
	"errors"
	"math"
)

var ErrInvalidTimeExpression = errors.New("invalid time expression")

type Era string

const (
	EraBCE Era = "BCE"
	EraCE  Era = "CE"
)

type TimeKind string

const (
	TimeExactDate TimeKind = "exact-date"
	TimeYear      TimeKind = "year"
	TimeCirca     TimeKind = "circa"
	TimeInterval  TimeKind = "interval"
)

type HistoricalYear struct {
	Era  Era `json:"era"`
	Year int `json:"year"`
}

type ExactDate struct {
	Era   Era `json:"era"`
	Year  int `json:"year"`
	Month int `json:"month"`
	Day   int `json:"day"`
}

type IntervalEndpoint struct {
	Era   Era  `json:"era"`
	Year  int  `json:"year"`
	Circa bool `json:"circa,omitempty"`
}

type TimeExpression struct {
	Kind  TimeKind          `json:"kind"`
	Date  *ExactDate        `json:"date,omitempty"`
	Year  *HistoricalYear   `json:"year,omitempty"`
	Start *IntervalEndpoint `json:"start,omitempty"`
	End   *IntervalEndpoint `json:"end,omitempty"`
}

func NormalizeTimeExpression(value TimeExpression) (TimeExpression, error) {
	switch value.Kind {
	case TimeExactDate:
		if value.Date == nil || value.Year != nil || value.Start != nil || value.End != nil || !validDate(*value.Date) {
			return TimeExpression{}, ErrInvalidTimeExpression
		}
	case TimeYear, TimeCirca:
		if value.Year == nil || value.Date != nil || value.Start != nil || value.End != nil || !validYear(*value.Year) {
			return TimeExpression{}, ErrInvalidTimeExpression
		}
	case TimeInterval:
		if value.Start == nil || value.End == nil || value.Date != nil || value.Year != nil ||
			!validEndpoint(*value.Start) || !validEndpoint(*value.End) {
			return TimeExpression{}, ErrInvalidTimeExpression
		}
		start, end, err := value.Coordinates()
		if err != nil || start > end {
			return TimeExpression{}, ErrInvalidTimeExpression
		}
	default:
		return TimeExpression{}, ErrInvalidTimeExpression
	}
	return value, nil
}

func (value TimeExpression) Coordinates() (float64, float64, error) {
	switch value.Kind {
	case TimeExactDate:
		if value.Date == nil || !validDate(*value.Date) {
			return 0, 0, ErrInvalidTimeExpression
		}
		coordinate := ExactDateCoordinate(*value.Date)
		return coordinate, coordinate, nil
	case TimeYear, TimeCirca:
		if value.Year == nil || !validYear(*value.Year) {
			return 0, 0, ErrInvalidTimeExpression
		}
		coordinate := HistoricalYearCoordinate(*value.Year)
		return coordinate, coordinate, nil
	case TimeInterval:
		if value.Start == nil || value.End == nil || !validEndpoint(*value.Start) || !validEndpoint(*value.End) {
			return 0, 0, ErrInvalidTimeExpression
		}
		start := HistoricalYearCoordinate(HistoricalYear{Era: value.Start.Era, Year: value.Start.Year})
		end := HistoricalYearCoordinate(HistoricalYear{Era: value.End.Era, Year: value.End.Year})
		if start > end {
			return 0, 0, ErrInvalidTimeExpression
		}
		return start, end, nil
	default:
		return 0, 0, ErrInvalidTimeExpression
	}
}

func HistoricalYearCoordinate(value HistoricalYear) float64 {
	if value.Era == EraBCE {
		return float64(1 - value.Year)
	}
	return float64(value.Year)
}

func ExactDateCoordinate(value ExactDate) float64 {
	base := HistoricalYearCoordinate(HistoricalYear{Era: value.Era, Year: value.Year})
	dayOfYear := value.Day
	for month := 1; month < value.Month; month++ {
		dayOfYear += daysInMonth(astronomicalYear(value.Era, value.Year), month)
	}
	days := 365
	if isLeapYear(astronomicalYear(value.Era, value.Year)) {
		days = 366
	}
	return base + float64(dayOfYear-1)/float64(days)
}

func validYear(value HistoricalYear) bool {
	return (value.Era == EraBCE || value.Era == EraCE) && value.Year >= 1 && value.Year <= 999999
}

func validEndpoint(value IntervalEndpoint) bool {
	return validYear(HistoricalYear{Era: value.Era, Year: value.Year})
}

func validDate(value ExactDate) bool {
	if !validYear(HistoricalYear{Era: value.Era, Year: value.Year}) || value.Month < 1 || value.Month > 12 {
		return false
	}
	return value.Day >= 1 && value.Day <= daysInMonth(astronomicalYear(value.Era, value.Year), value.Month)
}

func astronomicalYear(era Era, year int) int {
	if era == EraBCE {
		return 1 - year
	}
	return year
}

func isLeapYear(year int) bool {
	return divisible(year, 4) && (!divisible(year, 100) || divisible(year, 400))
}

func divisible(value, divisor int) bool {
	return int(math.Abs(float64(value)))%divisor == 0
}

func daysInMonth(year, month int) int {
	switch month {
	case 2:
		if isLeapYear(year) {
			return 29
		}
		return 28
	case 4, 6, 9, 11:
		return 30
	default:
		return 31
	}
}
