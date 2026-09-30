package creds

import (
	"os"
	"testing"
)

func TestLongestPrefixMatch(t *testing.T) {
	os.Setenv("BF_TEST_AUTH", "dXNlcjpwYXNz") // user:pass base64
	defer os.Unsetenv("BF_TEST_AUTH")

	tbl := Table{
		{Kind: Registry, Match: "registry.example.com", Auth: AuthRef{Env: "BF_TEST_AUTH"}},
		{Kind: Registry, Match: "registry.example.com/team-a", Auth: AuthRef{Env: "BF_TEST_AUTH"}},
		{Kind: Git, Match: "git.example.com", Auth: AuthRef{Env: "BF_TEST_AUTH"}},
	}
	tests := []struct {
		host string
		kind Kind
		want bool
	}{
		{"registry.example.com", Registry, true},
		{"registry.example.com/team-a/app", Registry, true}, // longest prefix
		{"git.example.com/org/repo", Git, true},
		{"docker.io", Registry, false}, // anonymous
	}
	for _, tt := range tests {
		if got := tbl.Match(tt.kind, tt.host); (got != nil) != tt.want {
			t.Fatalf("%s/%s: got=%v want=%v", tt.host, tt.kind, got, tt.want)
		}
	}
}

func TestResolve(t *testing.T) {
	os.Setenv("BF_TEST_AUTH", "dXNlcjpwYXNz")
	defer os.Unsetenv("BF_TEST_AUTH")
	c := &Credential{Kind: Registry, Match: "h", Auth: AuthRef{Env: "BF_TEST_AUTH"}}
	s, err := c.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if s.Username != "user" || s.Secret != "pass" {
		t.Fatalf("got %s/%s", s.Username, s.Secret)
	}
}
