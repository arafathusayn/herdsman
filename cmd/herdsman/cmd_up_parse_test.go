package main

import (
	"io"
	"testing"
)

func TestUpConfigMaySupplyTheCommands(t *testing.T) {
	if _, ok := parseUp([]string{"harbor", "-p", "go", "-c", "route.toml"}, io.Discard); !ok {
		t.Fatal("refused a config in place of -a and -o")
	}
	if _, ok := parseUp([]string{"harbor", "-p", "go"}, io.Discard); ok {
		t.Fatal("accepted no commands and no config")
	}
	if _, ok := parseUp([]string{"harbor", "-c", "route.toml"}, io.Discard); ok {
		t.Fatal("accepted no prompt")
	}
}
