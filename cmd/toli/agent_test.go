package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSkills(t *testing.T) {
	write := func(contents string) string {
		path := filepath.Join(t.TempDir(), "skills.json")
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	skills, err := readSkills(write(`[{"name": "Signups API", "description": "stats", "tags": ["api"], "examples": ["How many?"]}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 || skills[0].Name != "Signups API" || skills[0].Tags[0] != "api" || skills[0].Examples[0] != "How many?" {
		t.Errorf("readSkills = %+v", skills)
	}

	for _, tc := range []struct{ name, contents, want string }{
		{"unknown field", `[{"name": "x", "descripton": "typo"}]`, `unknown field "descripton"`},
		{"not an array", `{"name": "x"}`, "want a JSON array of skills"},
		{"trailing data", `[{"name": "x"}] [{"name": "y"}]`, "unexpected data after the skills"},
		{"not JSON", `name: x`, "invalid character"},
	} {
		if _, err := readSkills(write(tc.contents)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to say %q", tc.name, err, tc.want)
		}
	}
}
