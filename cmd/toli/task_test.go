package main

import "testing"

func TestMatchTask(t *testing.T) {
	ids := []string{
		"01a14331-e874-7c58-9f61-8f08992d96ac",
		"01a14332-e695-7aa6-ae82-39ef4bc6912b",
		"01a14333-0000-0000-0000-0000000096ac", // same last 4 as the first
	}
	for _, tc := range []struct{ id, want, err string }{
		{"01a14332-e695-7aa6-ae82-39ef4bc6912b", "01a14332-e695-7aa6-ae82-39ef4bc6912b", ""}, // full ID
		{"4bc6912b", "01a14332-e695-7aa6-ae82-39ef4bc6912b", ""},                             // short ID, as task list shows
		{"8f08992d96ac", "01a14331-e874-7c58-9f61-8f08992d96ac", ""},                         // more of it
		{"96ac", "", "96ac matches 2 tasks: use more of the ID"},
		{"deadbeef", "", "no task deadbeef"},
	} {
		got, err := matchTask(ids, tc.id)
		if tc.err != "" {
			if err == nil || err.Error() != tc.err {
				t.Errorf("matchTask(%q) error = %v, want %q", tc.id, err, tc.err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("matchTask(%q) = %q, %v; want %q", tc.id, got, err, tc.want)
		}
	}
}
