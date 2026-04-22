package refinery

import "testing"

func TestCategorizeFile(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"main.go", "go"},
		{"src/App.tsx", "web"},
		{"handler.py", "py"},
		{"README.md", "docs"},
		{"config.yaml", "config"},
		{"schema.sql", "sql"},
		{"build.sh", "script"},
		{"foo_test.go", "go"}, // extension wins over "test" substring
		{"some/test_helpers/util.js", "web"},
		{"Dockerfile", "other"},
	}
	for _, c := range cases {
		if got := categorizeFile(c.in); got != c.want {
			t.Errorf("categorizeFile(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestInferFileCategory_PicksMostCommon(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "any"},
		{"[]", "any"},
		{"null", "any"},
		{`["a.go","b.go","c.md"]`, "go"},
		{`["a.md","b.md","c.go"]`, "docs"},
		{`["a.py"]`, "py"},
	}
	for _, c := range cases {
		if got := inferFileCategory(c.in); got != c.want {
			t.Errorf("inferFileCategory(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPatternNameWithCategory(t *testing.T) {
	if got := patternNameWithCategory([]string{"grep", "read", "edit"}, "go"); got != "pat[go]: grep→read→edit" {
		t.Errorf("unexpected name: %s", got)
	}
	if got := patternNameWithCategory([]string{"grep", "read"}, "any"); got != "pat: grep→read" {
		t.Errorf("unexpected name: %s", got)
	}
}
