package blacklist

import (
	"reflect"
	"testing"
)

func TestRemoveGroupNormalizesWordAndKeepsRemainingMatcher(t *testing.T) {
	t.Parallel()

	bl := New()
	bl.AddGroup(-100123, "Spam")
	bl.AddGroup(-100123, "Eggs")

	if removed := bl.RemoveGroup(-100123, "  SPAM "); !removed {
		t.Fatal("RemoveGroup() = false, want true for normalized group word")
	}
	if got, want := bl.ListGroup(-100123), []string{"eggs"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ListGroup() = %v, want %v after removing one of two words", got, want)
	}
	if matched := bl.MatchFieldsWithGroup(-100123, "fresh", "EGGS"); matched != "eggs" {
		t.Fatalf("MatchFieldsWithGroup() = %q, want %q for remaining group matcher", matched, "eggs")
	}
	if matched := bl.MatchFieldsWithGroup(-100123, "spam"); matched != "" {
		t.Fatalf("MatchFieldsWithGroup() = %q, want empty after removal", matched)
	}
}

func TestMatchFieldsWithGroupFallsBackToGroupMatcherAcrossFields(t *testing.T) {
	t.Parallel()

	bl := New()
	bl.AddGroup(-100123, "alice bob")

	if matched := bl.MatchFieldsWithGroup(-100123, "Alice", "Bob"); matched != "alice bob" {
		t.Fatalf("MatchFieldsWithGroup() = %q, want %q across field boundary", matched, "alice bob")
	}
	if matched := bl.MatchFieldsWithGroup(-999999, "Alice", "Bob"); matched != "" {
		t.Fatalf("MatchFieldsWithGroup() for other chat = %q, want empty", matched)
	}
}

func TestGlobalAndGroupNoOpBranchesReturnEmptyMatches(t *testing.T) {
	t.Parallel()

	bl := New()
	bl.Add("   ")
	bl.AddGroup(-100123, "   ")

	if got := bl.List(); len(got) != 0 {
		t.Fatalf("List() = %v, want empty after blank Add()", got)
	}
	if got := bl.ListGroup(-100123); got != nil {
		t.Fatalf("ListGroup() = %v, want nil after blank AddGroup()", got)
	}
	if matched := bl.MatchFields("hello"); matched != "" {
		t.Fatalf("MatchFields() = %q, want empty without a matcher", matched)
	}
	if matched := bl.MatchFieldsWithGroup(-100123, "hello"); matched != "" {
		t.Fatalf("MatchFieldsWithGroup() = %q, want empty without global/group matchers", matched)
	}

	var nilMatcher *matcher
	if matched := nilMatcher.MatchFields("hello"); matched != "" {
		t.Fatalf("(*matcher)(nil).MatchFields() = %q, want empty", matched)
	}
}

func TestRemoveGroupDeletesLastWordAndClearsMatcher(t *testing.T) {
	t.Parallel()

	bl := New()
	bl.AddGroup(-100123, "OnlyWord")

	if removed := bl.RemoveGroup(-100123, " onlyword "); !removed {
		t.Fatal("RemoveGroup() = false, want true for last group word")
	}
	if got := bl.ListGroup(-100123); got != nil {
		t.Fatalf("ListGroup() = %v, want nil after removing final word", got)
	}
	if matched := bl.MatchWithGroup(-100123, "onlyword"); matched != "" {
		t.Fatalf("MatchWithGroup() = %q, want empty after removing final word", matched)
	}
}

func TestMatchWithGroupFallsBackToGroupMatcherWhenGlobalMisses(t *testing.T) {
	t.Parallel()

	bl := New()
	bl.Add("global")
	bl.AddGroup(-100123, "group")

	if matched := bl.MatchWithGroup(-100123, "contains group"); matched != "group" {
		t.Fatalf("MatchWithGroup() = %q, want %q from group matcher", matched, "group")
	}
}

func TestFoldMatcherRuneCoversASCIIAndUnicodeBranches(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   rune
		want rune
	}{
		{name: "ascii uppercase", in: 'A', want: 'a'},
		{name: "ascii lowercase", in: 'b', want: 'b'},
		{name: "unicode uppercase", in: 'Ä', want: 'ä'},
		{name: "unicode unchanged", in: '你', want: '你'},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := foldMatcherRune(tc.in); got != tc.want {
				t.Fatalf("foldMatcherRune(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
