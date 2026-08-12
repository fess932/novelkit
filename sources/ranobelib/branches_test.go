package ranobelib_test

import (
	"testing"

	"github.com/fess932/novelkit/sources/ranobelib"
)

func chapter(index int, branchIDs []int, team, user string) ranobelib.ChapterInfo {
	ci := ranobelib.ChapterInfo{Index: index, Volume: "1", Number: "1"}
	for _, id := range branchIDs {
		b := ranobelib.ChapterBranch{}
		if id != 0 {
			v := id
			b.BranchID = &v
		}
		if team != "" {
			b.Teams = []ranobelib.Named{{Name: team}}
		}
		b.User.Username = user
		ci.Branches = append(ci.Branches, b)
	}
	return ci
}

// Branch captions come from the cards: a branch may have several teams, while
// the chapter list names only the one that posted a given chapter.
func TestCollectBranchesUsesCards(t *testing.T) {
	chapters := []ranobelib.ChapterInfo{
		chapter(1, []int{9824}, "Silent Step", "Theunt"),
		chapter(2, []int{9824}, "Silent Step", "AtLas"),
		chapter(3, []int{11722}, "Lipov Team", "Andrey"),
	}
	cards := []ranobelib.BranchCard{
		{ID: 9824, Name: "Ничоси 2", Teams: []ranobelib.Named{{Name: "Silent Step"}, {Name: "Эрл Грей"}}},
		{ID: 11722, Name: "Webfandom", Teams: []ranobelib.Named{{Name: "Aniker Team"}, {Name: "Lipov Team"}}},
		{ID: 26435, Name: "Alternative translation"}, // a tab on the site, but no chapters
	}

	got := ranobelib.CollectBranches(chapters, cards)
	if len(got) != 3 {
		t.Fatalf("expected 3 branches, got %d: %+v", len(got), got)
	}
	// The fullest one comes first.
	if got[0].ID != 9824 || got[0].Count != 2 {
		t.Errorf("branches sorted wrong: %+v", got[0])
	}
	if label := got[0].Label(); label != "Silent Step & Эрл Грей" {
		t.Errorf("the branch caption must come from the card, got %q", label)
	}
	if len(got[0].Uploaders) != 2 {
		t.Errorf("uploaders were not collected: %+v", got[0].Uploaders)
	}

	var empty ranobelib.Branch
	for _, b := range got {
		if b.ID == 26435 {
			empty = b
		}
	}
	if empty.Count != 0 || empty.Label() != "Alternative translation" {
		t.Errorf("an empty branch must stay in the list with zero chapters: %+v", empty)
	}
}

// A branch without an identifier (the site sends null) must be reachable as 0.
func TestCollectBranchesNullID(t *testing.T) {
	got := ranobelib.CollectBranches([]ranobelib.ChapterInfo{
		chapter(1, []int{0}, "sAnTeLa", "sAnTeLa"),
	}, nil)

	if len(got) != 1 || got[0].ID != 0 || got[0].Count != 1 {
		t.Fatalf("a branch without an id parsed wrong: %+v", got)
	}
	if got[0].Label() != "sAnTeLa" {
		t.Errorf("the caption must come from the chapter team, got %q", got[0].Label())
	}
}

func TestChaptersOfBranch(t *testing.T) {
	chapters := []ranobelib.ChapterInfo{
		chapter(3, []int{1, 2}, "", ""),
		chapter(1, []int{1}, "", ""),
		chapter(2, []int{2}, "", ""),
	}

	got := ranobelib.ChaptersOfBranch(chapters, 1)
	if len(got) != 2 {
		t.Fatalf("expected 2 chapters in the branch, got %d", len(got))
	}
	if got[0].Index != 1 || got[1].Index != 3 {
		t.Errorf("chapters are not in reading order: %+v", got)
	}
}

func TestParseSlug(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"https://ranobelib.me/ru/book/14841--beginning-after-the-end-novel", "14841--beginning-after-the-end-novel", true},
		// The site serves the same book under several sections, so the slug is
		// found by shape rather than by a known prefix.
		{"https://ranobelib.me/ru/manga/14841--beginning-after-the-end-novel", "14841--beginning-after-the-end-novel", true},
		{"https://ranobelib.me/ru/manga/14841--x?section=chapters", "14841--x", true},
		{"ranobelib.me/en/anything/deeper/14841--x/read/v1/c2", "14841--x", true},
		{"https://ranobelib.me/ru/manga", "", false},
		{"https://ranobelib.me/", "", false},
		{"https://ranobelib.me/ru/book/14841--beginning-after-the-end-novel?section=chapters", "14841--beginning-after-the-end-novel", true},
		{"14841--beginning-after-the-end-novel", "14841--beginning-after-the-end-novel", true},
		{"a plain title with spaces", "", false},
		{"beginning-after-the-end", "", false},
	}
	for _, c := range cases {
		got, ok := ranobelib.ParseSlug(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseSlug(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestBranchTranslators(t *testing.T) {
	b := ranobelib.Branch{
		Teams:     []string{"Silent Step", "Эрл Грей"},
		Uploaders: []string{"Theunt", "Silent Step"},
	}
	got := b.Translators()
	want := []string{"Silent Step", "Эрл Грей", "Theunt"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
