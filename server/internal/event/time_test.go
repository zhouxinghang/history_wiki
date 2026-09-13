package event

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestExactDateCoordinatesUseSharedGregorianVectors(t *testing.T) {
	type vector struct {
		Name       string  `json:"name"`
		Era        Era     `json:"era"`
		Year       int     `json:"year"`
		Month      int     `json:"month"`
		Day        int     `json:"day"`
		Coordinate float64 `json:"coordinate"`
	}
	contents, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "time-coordinate-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors []vector
	if err := json.Unmarshal(contents, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, test := range vectors {
		t.Run(test.Name, func(t *testing.T) {
			date := ExactDate{Era: test.Era, Year: test.Year, Month: test.Month, Day: test.Day}
			if !validDate(date) {
				t.Fatal("shared vector was rejected as an invalid date")
			}
			if got := ExactDateCoordinate(date); math.Abs(got-test.Coordinate) > 1e-12 {
				t.Fatalf("coordinate = %.15f, want %.15f", got, test.Coordinate)
			}
		})
	}
}

func TestTimeExpressionValidation(t *testing.T) {
	valid := []TimeExpression{
		{Kind: TimeExactDate, Date: &ExactDate{Era: EraCE, Year: 2000, Month: 2, Day: 29}},
		{Kind: TimeYear, Year: &HistoricalYear{Era: EraBCE, Year: 221}},
		{Kind: TimeCirca, Year: &HistoricalYear{Era: EraCE, Year: 100}},
		{Kind: TimeInterval, Start: &IntervalEndpoint{Era: EraBCE, Year: 1}, End: &IntervalEndpoint{Era: EraCE, Year: 1}},
	}
	for _, expression := range valid {
		if _, err := NormalizeTimeExpression(expression); err != nil {
			t.Fatalf("valid expression %#v: %v", expression, err)
		}
	}

	invalid := []TimeExpression{
		{Kind: TimeExactDate, Date: &ExactDate{Era: EraCE, Year: 1900, Month: 2, Day: 29}},
		{Kind: TimeYear, Year: &HistoricalYear{Era: EraCE, Year: 0}},
		{Kind: TimeInterval, Start: &IntervalEndpoint{Era: EraCE, Year: 2}, End: &IntervalEndpoint{Era: EraCE, Year: 1}},
		{Kind: TimeYear, Year: &HistoricalYear{Era: EraCE, Year: 1}, Date: &ExactDate{Era: EraCE, Year: 1, Month: 1, Day: 1}},
	}
	for _, expression := range invalid {
		if _, err := NormalizeTimeExpression(expression); err == nil {
			t.Fatalf("invalid expression accepted: %#v", expression)
		}
	}
}
