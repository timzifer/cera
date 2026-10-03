package cera

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// budgetTable renders the table of budgets as the README documents it.
func budgetTable() string {
	var b strings.Builder
	b.WriteString("| bound | value | key | what |\n|---|---:|---|---|\n")
	for _, x := range budgets {
		key := "–"
		if x.key != "" {
			key = "`" + x.key + "`"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", x.name, limitString(x.limit), key, x.what)
	}
	return b.String()
}

func limitString(v int) string {
	switch {
	case v >= 1<<20 && v%(1<<20) == 0:
		return fmt.Sprintf("%d Mi", v>>20)
	case v >= 1<<10 && v%(1<<10) == 0:
		return fmt.Sprintf("%d Ki", v>>10)
	}
	return fmt.Sprint(v)
}

// TestBudgetsDocumented checks that the README carries the table of
// budgets as budget.go defines it.
func TestBudgetsDocumented(t *testing.T) {
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	want := budgetTable()
	if !strings.Contains(string(readme), want) {
		t.Errorf("README.md does not carry the table of budgets; it should read:\n\n%s", want)
	}
	seen := map[string]bool{}
	for _, x := range budgets {
		if seen[x.name] {
			t.Errorf("%s listed twice", x.name)
		}
		seen[x.name] = true
		if x.limit <= 0 {
			t.Errorf("%s: limit %d", x.name, x.limit)
		}
	}
}

// TestBudgetsCounted checks that reaching a budget draws the page and
// counts its key.
func TestBudgetsCounted(t *testing.T) {
	deep := strings.Repeat("q ", maxStateDepth+10) + "1 0 0 rg 0 0 10 10 re f"
	annots := make([]string, maxAnnots+1)
	var refs []string
	for i := range annots {
		annots[i] = "<< /Subtype /Square /Rect [0 0 5 5] /C [1 0 0] >>"
		refs = append(refs, fmt.Sprintf("%d 0 R", 100+i))
	}
	for _, c := range []struct {
		key  string
		data []byte
	}{
		{"nesting-budget", buildPDF([]string{deep}, "")},
		{"nesting-budget", buildPDF([]string{"/X Do"}, "/Resources << /XObject << /X 100 0 R >> >>",
			stream("/Type /XObject /Subtype /Form /BBox [0 0 10 10] /Resources << /XObject << /X 100 0 R >> >>", "/X Do"))},
		{"mesh-budget", buildPDF([]string{"/S sh"}, "/Resources << /Shading << /S 100 0 R >> >>",
			streamObj("/ShadingType 5 /VerticesPerRow 100000 "+meshDict, make([]byte, 64)))},
		{"annot-budget", buildPDF([]string{""}, "/Annots ["+strings.Join(refs, " ")+"]", annots...)},
	} {
		_, st, err := renderPage(t, c.data, 0, RenderOptions{Scale: 0.2})
		if err != nil {
			t.Errorf("%s: %v", c.key, err)
		}
		if st.Unsupported[c.key] == 0 {
			t.Errorf("%s not counted: %v", c.key, st.Unsupported)
		}
	}
}
