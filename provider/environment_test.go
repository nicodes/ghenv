package provider

import (
	"slices"
	"testing"
)

func TestDevelopmentEnvironment(t *testing.T) {
	result := DevelopmentEnvironment([]string{"PATH=/tools", "HOME=/developer", "TERM=xterm", "GH_TOKEN=parent-token", "RUNTIME_DATABASE_URL=remote", "OPENAI_API_KEY=production", "BASH_ENV=/unsafe", "NODE_OPTIONS=--require=unsafe", "MAKEFLAGS=CLERK_SECRET_KEY=old", "EXPO_PUBLIC_API_URL=https://remote", "EXPO_NO_DOTENV=0"}, map[string]string{"OPENAI_API_KEY": "preview"})
	for _, want := range []string{"PATH=/tools", "HOME=/developer", "TERM=xterm", "OPENAI_API_KEY=preview", "EXPO_NO_DOTENV=1"} {
		if !slices.Contains(result, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if len(result) != 5 {
		t.Fatal("unapproved parent settings reached child")
	}
}
