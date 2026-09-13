package catalog

import "testing"

func TestNormalizeCanonicalEntityFieldsAllowsSameNameWithDisambiguation(t *testing.T) {
	label := " 西汉皇帝 "
	name, normalizedLabel, err := NormalizeCanonicalEntityFields(" 刘邦 ", &label)
	if err != nil {
		t.Fatal(err)
	}
	if name != "刘邦" || normalizedLabel == nil || *normalizedLabel != "西汉皇帝" {
		t.Fatalf("normalized fields = %q, %#v", name, normalizedLabel)
	}

	empty := "  "
	_, normalizedLabel, err = NormalizeCanonicalEntityFields("丝绸之路", &empty)
	if err != nil || normalizedLabel != nil {
		t.Fatalf("blank label = %#v, error = %v", normalizedLabel, err)
	}
}

func TestCanonicalEntityKindsResolveOnlyKnownTables(t *testing.T) {
	for kind, want := range map[CanonicalEntityKind]string{
		KindRegion:           "regions",
		KindPlace:            "places",
		KindHistoricalPeriod: "historical_periods",
		KindHistoricalFigure: "historical_figures",
		KindTopicTag:         "topic_tags",
	} {
		got, err := CanonicalEntityTable(kind)
		if err != nil || got != want {
			t.Fatalf("table for %q = %q, %v", kind, got, err)
		}
	}
	if _, err := CanonicalEntityTable("events"); err == nil {
		t.Fatal("unknown entity kind unexpectedly resolved to a table")
	}
}
