package toolcall

import "testing"

func TestReadinessParity(t *testing.T) {
	cases := []struct {
		name string
		args string
		want bool
	}{
		{"Update", `{"file_path":"/x","old_string":"a","new_string":""}`, true},
		{"Edit", `{"file_path":"/x","old_string":"a","new_string":""}`, true},
		{"Update", `{"file_path":"/x","old_string":"a"}`, false},
		{"Read", `{"file_path":""}`, false},
		{"Read", `{"file_path":"/x"}`, true},
		{"TaskUpdate", `{"taskId":"1","status":"completed"}`, true},
		{"mcp__x__Update", `{"file_path":"/x"}`, false},
	}
	for _, tc := range cases {
		if got := CompleteJSON(tc.args, tc.name); got != tc.want {
			t.Errorf("%s CompleteJSON=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestCanonicalName(t *testing.T) {
	if got := CanonicalName("Update", []string{"Edit", "Read"}); got != "Edit" {
		t.Fatalf("got %q", got)
	}
	if got := CanonicalName("TaskUpdate", []string{"Edit"}); got != "TaskUpdate" {
		t.Fatalf("protected tool changed to %q", got)
	}
}

func TestLaterConflictingAliasWins(t *testing.T) {
	raw := `{"file_path":"/wrong","path":"/right","oldString":"a","newString":""}`
	got := NormalizeJSON(raw, "Update")
	want := `{"file_path":"/right","old_string":"a","new_string":""}`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if !CompleteJSON(got, "Update") {
		t.Fatalf("normalized update is incomplete: %s", got)
	}
}

func TestMergeCompleteRewriteWins(t *testing.T) {
	current := `{"path":"/wrong"}`
	incoming := `{"file_path":"/right","old_string":"a","new_string":""}`
	if got := Merge(current, incoming, "Update"); got != incoming {
		t.Fatalf("got %s", got)
	}
}

func FuzzNormalizeNeverPanics(f *testing.F) {
	for _, seed := range []string{"", `{`, `{}`, `{"path":"/x"}`, "\xff", `[]`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		_ = NormalizeJSON(raw, "Update")
		_ = CompleteJSON(raw, "Update")
	})
}
