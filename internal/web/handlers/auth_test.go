package handlers

import "testing"

func TestSafeNextRejectsCrossHostTargets(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"/rules", "/rules"},
		{"/containers/abc/stop", "/containers/abc/stop"},
		{"//evil.example", ""},
		{"/\\evil.example", ""},
		{"/\tevil.example", ""},
		{"/\r\n/evil.example", ""},
		{"https://evil.example", ""},
		{"evil.example", ""},
	}
	for _, tc := range cases {
		if got := safeNext(tc.in); got != tc.want {
			t.Errorf("safeNext(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
