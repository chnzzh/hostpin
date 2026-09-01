package main

import "testing"

func TestHelpCommandsDoNotStartServer(t *testing.T) {
	for _, arguments := range [][]string{{"--help"}, {"serve", "--help"}, {"migrate", "sqlite-to-postgres", "--help"}} {
		if err := run(arguments); err != nil {
			t.Fatalf("run(%q) returned %v", arguments, err)
		}
	}
}
