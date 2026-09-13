package store

import (
	"testing"

	historyevent "github.com/zhouxinghang/history_wiki/server/internal/event"
)

func TestLiteralSubstringPatternEscapesLikeMetacharacters(t *testing.T) {
	if got, want := literalSubstringPattern(`  100%_\Path  `), `%100\%\_\\Path%`; got != want {
		t.Fatalf("literal substring pattern = %q, want %q", got, want)
	}
	if got := literalSubstringPattern("   "); got != "" {
		t.Fatalf("blank literal substring pattern = %q", got)
	}
}

func TestSortPublishedEventQueryCandidatesUsesChineseTitleThenUUID(t *testing.T) {
	candidates := []publishedEventQueryCandidate{
		queryCandidate("00000000-0000-7000-8000-000000000003", "中原", 100, 1),
		queryCandidate("00000000-0000-7000-8000-000000000002", "北京", 100, 1),
		queryCandidate("00000000-0000-7000-8000-000000000001", "北京", 100, 1),
		queryCandidate("00000000-0000-7000-8000-000000000004", "阿里", 100, 1),
		queryCandidate("00000000-0000-7000-8000-000000000005", "更早", 99, 9),
	}

	sortPublishedEventQueryCandidates(candidates)
	want := []string{
		"00000000-0000-7000-8000-000000000005",
		"00000000-0000-7000-8000-000000000004",
		"00000000-0000-7000-8000-000000000001",
		"00000000-0000-7000-8000-000000000002",
		"00000000-0000-7000-8000-000000000003",
	}
	for index := range want {
		if got := candidates[index].Event.ID; got != want[index] {
			t.Fatalf("candidate %d = %s, want %s", index, got, want[index])
		}
	}
}

func queryCandidate(id, title string, start float64, displayOrder int) publishedEventQueryCandidate {
	return publishedEventQueryCandidate{
		Event: historyevent.PublishedEvent{
			ID: id,
			Revision: historyevent.Revision{
				Title: title, DisplayOrder: displayOrder,
			},
		},
		StartCoordinate: start,
	}
}
