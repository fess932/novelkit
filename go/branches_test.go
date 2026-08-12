package ranobelib_test

import (
	"testing"

	"github.com/fess932/ranobelib"
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

// Подписи веток берутся из карточек: у ветки бывает несколько команд,
// а в списке глав указана только та, что залила конкретную главу.
func TestCollectBranchesUsesCards(t *testing.T) {
	chapters := []ranobelib.ChapterInfo{
		chapter(1, []int{9824}, "Silent Step", "Theunt"),
		chapter(2, []int{9824}, "Silent Step", "AtLas"),
		chapter(3, []int{11722}, "Lipov Team", "Andrey"),
	}
	cards := []ranobelib.BranchCard{
		{ID: 9824, Name: "Ничоси 2", Teams: []ranobelib.Named{{Name: "Silent Step"}, {Name: "Эрл Грей"}}},
		{ID: 11722, Name: "Webfandom", Teams: []ranobelib.Named{{Name: "Aniker Team"}, {Name: "Lipov Team"}}},
		{ID: 26435, Name: "Альтернативный перевод"}, // вкладка на сайте есть, глав нет
	}

	got := ranobelib.CollectBranches(chapters, cards)
	if len(got) != 3 {
		t.Fatalf("ожидалось 3 ветки, получено %d: %+v", len(got), got)
	}
	// Самая полная — первой.
	if got[0].ID != 9824 || got[0].Count != 2 {
		t.Errorf("ветки отсортированы неверно: %+v", got[0])
	}
	if label := got[0].Label(); label != "Silent Step & Эрл Грей" {
		t.Errorf("подпись ветки должна браться из карточки, получено %q", label)
	}
	if len(got[0].Uploaders) != 2 {
		t.Errorf("не собраны заливавшие: %+v", got[0].Uploaders)
	}

	var empty ranobelib.Branch
	for _, b := range got {
		if b.ID == 26435 {
			empty = b
		}
	}
	if empty.Count != 0 || empty.Label() != "Альтернативный перевод" {
		t.Errorf("пустая ветка должна остаться в списке с нулём глав: %+v", empty)
	}
}

// Ветка без идентификатора (сайт присылает null) должна быть доступна как 0.
func TestCollectBranchesNullID(t *testing.T) {
	got := ranobelib.CollectBranches([]ranobelib.ChapterInfo{
		chapter(1, []int{0}, "sAnTeLa", "sAnTeLa"),
	}, nil)

	if len(got) != 1 || got[0].ID != 0 || got[0].Count != 1 {
		t.Fatalf("ветка без id разобрана неверно: %+v", got)
	}
	if got[0].Label() != "sAnTeLa" {
		t.Errorf("подпись должна взяться из команды главы, получено %q", got[0].Label())
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
		t.Fatalf("ожидалось 2 главы ветки, получено %d", len(got))
	}
	if got[0].Index != 1 || got[1].Index != 3 {
		t.Errorf("главы не отсортированы по порядку чтения: %+v", got)
	}
}

func TestParseSlug(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"https://ranobelib.me/ru/book/14841--beginning-after-the-end-novel", "14841--beginning-after-the-end-novel", true},
		{"https://ranobelib.me/ru/book/14841--beginning-after-the-end-novel?section=chapters", "14841--beginning-after-the-end-novel", true},
		{"14841--beginning-after-the-end-novel", "14841--beginning-after-the-end-novel", true},
		{"Начало после конца", "", false},
		{"beginning-after-the-end", "", false},
	}
	for _, c := range cases {
		got, ok := ranobelib.ParseSlug(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseSlug(%q) = (%q, %v), ожидалось (%q, %v)", c.in, got, ok, c.want, c.ok)
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
		t.Fatalf("получено %v, ожидалось %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("получено %v, ожидалось %v", got, want)
		}
	}
}
