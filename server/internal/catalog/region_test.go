package catalog

import "testing"

func TestNormalizeRegionFieldsAllowsSameNameWithOptionalDisambiguation(t *testing.T) {
	label := " 东亚地理语境 "
	name, normalizedLabel, err := NormalizeRegionFields(" 东亚 ", &label)
	if err != nil {
		t.Fatal(err)
	}
	if name != "东亚" || normalizedLabel == nil || *normalizedLabel != "东亚地理语境" {
		t.Fatalf("normalized fields = %q, %#v", name, normalizedLabel)
	}

	empty := "  "
	_, normalizedLabel, err = NormalizeRegionFields("东亚", &empty)
	if err != nil || normalizedLabel != nil {
		t.Fatalf("blank label = %#v, error = %v", normalizedLabel, err)
	}
}

func TestEntityStatusSeparatesWritableAndQueryableStates(t *testing.T) {
	if !StatusMerged.ValidFilter() {
		t.Fatal("merged status should be queryable")
	}
	if StatusMerged.Writable() {
		t.Fatal("merged status must be reserved for the later merge workflow")
	}
}
