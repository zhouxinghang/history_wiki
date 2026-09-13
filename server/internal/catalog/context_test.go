package catalog

import (
	"reflect"
	"testing"
)

func TestNormalizeContextEntityFieldsAndRegionIDs(t *testing.T) {
	label := " 战国时期语境 "
	name, normalizedLabel, err := NormalizeHistoricalPeriodFields(" 战国 ", &label)
	if err != nil {
		t.Fatal(err)
	}
	if name != "战国" || normalizedLabel == nil || *normalizedLabel != "战国时期语境" {
		t.Fatalf("normalized historical period = %q, %#v", name, normalizedLabel)
	}

	got := NormalizeRegionIDs([]string{" region-b ", "region-a", "region-b", ""})
	want := []string{"region-a", "region-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized region IDs = %#v, want %#v", got, want)
	}
}
