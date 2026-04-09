package blacklist_test

import (
	"reflect"
	"sync"
	"testing"

	"github.com/qwq233/fuckadbot/internal/blacklist"
)

func TestMatchGlobal(t *testing.T) {
	t.Parallel()

	bl := blacklist.New()
	bl.Add("spam")

	if matched := bl.Match("buy cheap spam now"); matched != "spam" {
		t.Fatalf("Match() = %q, want %q", matched, "spam")
	}
}

func TestMatchMissesWhenWordAbsent(t *testing.T) {
	t.Parallel()

	bl := blacklist.New()
	bl.Add("spam")

	if matched := bl.Match("hello world"); matched != "" {
		t.Fatalf("Match() = %q, want empty", matched)
	}
}

func TestMatchCaseInsensitive(t *testing.T) {
	t.Parallel()

	bl := blacklist.New()
	bl.Add("SPAM")

	if matched := bl.Match("Buy Cheap SPAM NOW"); matched != "spam" {
		t.Fatalf("Match() = %q, want %q", matched, "spam")
	}
}

func TestMatchIgnoresEmptyWord(t *testing.T) {
	t.Parallel()

	bl := blacklist.New()
	bl.Load([]string{"", "  "}) // should be ignored

	if matched := bl.Match("anything"); matched != "" {
		t.Fatalf("Match() = %q, want empty for blank-word blacklist", matched)
	}
}

func TestMatchWithGroupGlobal(t *testing.T) {
	t.Parallel()

	bl := blacklist.New()
	bl.Add("globalword")

	if matched := bl.MatchWithGroup(-100123, "contains globalword"); matched != "globalword" {
		t.Fatalf("MatchWithGroup() = %q, want %q", matched, "globalword")
	}
}

func TestMatchWithGroupScoped(t *testing.T) {
	t.Parallel()

	bl := blacklist.New()
	bl.AddGroup(-100123, "groupword")

	// Should match in the target group.
	if matched := bl.MatchWithGroup(-100123, "contains groupword"); matched != "groupword" {
		t.Fatalf("MatchWithGroup() in target group = %q, want %q", matched, "groupword")
	}

	// Should NOT match in a different group.
	if matched := bl.MatchWithGroup(-999, "contains groupword"); matched != "" {
		t.Fatalf("MatchWithGroup() in other group = %q, want empty", matched)
	}
}

func TestMatchWithGroupGlobalWordSeenAcrossGroups(t *testing.T) {
	t.Parallel()

	bl := blacklist.New()
	bl.Add("globalword")

	for _, chatID := range []int64{-100, -200, -300} {
		if matched := bl.MatchWithGroup(chatID, "contains globalword"); matched != "globalword" {
			t.Fatalf("MatchWithGroup(chat=%d) = %q, want %q", chatID, matched, "globalword")
		}
	}
}

func TestRemoveGlobal(t *testing.T) {
	t.Parallel()

	bl := blacklist.New()
	bl.Add("spam")

	if removed := bl.Remove("spam"); !removed {
		t.Fatal("Remove() = false, want true")
	}
	if matched := bl.Match("spam"); matched != "" {
		t.Fatalf("Match() after Remove() = %q, want empty", matched)
	}
}

func TestRemoveGroupScoped(t *testing.T) {
	t.Parallel()

	bl := blacklist.New()
	bl.AddGroup(-100123, "groupword")

	if removed := bl.RemoveGroup(-100123, "groupword"); !removed {
		t.Fatal("RemoveGroup() = false, want true")
	}
	if matched := bl.MatchWithGroup(-100123, "contains groupword"); matched != "" {
		t.Fatalf("MatchWithGroup() after RemoveGroup() = %q, want empty", matched)
	}
}

// TestBlacklistConcurrentAccess exercises concurrent reads, writes, and
// removals. Run with -race to detect data races.
func TestBlacklistConcurrentAccess(t *testing.T) {
	t.Parallel()

	bl := blacklist.New()
	bl.Add("seed")
	bl.AddGroup(-100, "gseed")

	var wg sync.WaitGroup
	const goroutines = 100

	wg.Add(goroutines)
	for i := range goroutines {
		go func(id int) {
			defer wg.Done()

			switch id % 6 {
			case 0:
				bl.Add("word")
			case 1:
				bl.Remove("word")
			case 2:
				bl.Match("contains word here")
			case 3:
				bl.List()
			case 4:
				bl.MatchWithGroup(-100, "test gseed here")
			case 5:
				bl.AddGroup(int64(-100-id), "dynamic")
			}
		}(i)
	}

	wg.Wait()
}

func TestLoadNormalizesDeduplicatesAndSortsGlobalWords(t *testing.T) {
	t.Parallel()

	bl := blacklist.New()
	bl.Load([]string{"  Spam  ", "eggs", "spam", "", " EGGS "})

	got := bl.List()
	want := []string{"eggs", "spam"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %v, want %v", got, want)
	}
}

func TestLoadGroupNormalizesDeduplicatesAndSortsWords(t *testing.T) {
	t.Parallel()

	bl := blacklist.New()
	bl.LoadGroup(-100123, []string{"  GroupWord  ", "apple", "groupword", " APPLE "})

	got := bl.ListGroup(-100123)
	want := []string{"apple", "groupword"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListGroup() = %v, want %v", got, want)
	}
}

func TestRemoveGroupDeletesEmptyGroupState(t *testing.T) {
	t.Parallel()

	bl := blacklist.New()
	bl.AddGroup(-100123, "onlyword")

	if removed := bl.RemoveGroup(-100123, "onlyword"); !removed {
		t.Fatal("RemoveGroup() = false, want true")
	}
	if got := bl.ListGroup(-100123); got != nil {
		t.Fatalf("ListGroup() = %v, want nil after removing final word", got)
	}
	if matched := bl.MatchWithGroup(-100123, "onlyword"); matched != "" {
		t.Fatalf("MatchWithGroup() = %q, want empty after removing final group word", matched)
	}
}

func TestRemoveGroupReturnsFalseForMissingChatOrWord(t *testing.T) {
	t.Parallel()

	bl := blacklist.New()
	bl.AddGroup(-100123, "present")

	if removed := bl.RemoveGroup(-200000, "present"); removed {
		t.Fatal("RemoveGroup() = true, want false for missing chat")
	}
	if removed := bl.RemoveGroup(-100123, "missing"); removed {
		t.Fatal("RemoveGroup() = true, want false for missing word")
	}
	if got := bl.ListGroup(-100123); !reflect.DeepEqual(got, []string{"present"}) {
		t.Fatalf("ListGroup() = %v, want original group contents preserved", got)
	}
}

func TestMatchReturnsDeterministicDirectMatchForOverlappingWords(t *testing.T) {
	t.Parallel()

	bl := blacklist.New()
	bl.Load([]string{"he", "hers", "she"})

	if matched := bl.Match("ushers"); matched != "she" {
		t.Fatalf("Match() = %q, want %q for overlapping keywords", matched, "she")
	}
}
