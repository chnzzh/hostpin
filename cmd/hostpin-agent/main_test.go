package main

import "testing"

func TestHelpCommandsDoNotStartAgent(t *testing.T) {
	for _, arguments := range [][]string{{"--help"}, {"run", "--help"}, {"install", "--help"}, {"collect", "--help"}} {
		if err := run(arguments); err != nil {
			t.Fatalf("run(%q) returned %v", arguments, err)
		}
	}
}
