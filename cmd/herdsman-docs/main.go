// herdsman-docs writes the generated pages under docs/ from the saved output
// of `herdsman spec cli`, `spec descriptor` and `spec proto`. Run it through
// `make docs`.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/arafathusayn/herdsman/internal/docgen"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("herdsman-docs", flag.ContinueOnError)
	flags.SetOutput(stderr)
	cli := flags.String("cli", "", "file holding the output of spec cli")
	descriptor := flags.String("descriptor", "", "file holding the output of spec descriptor")
	source := flags.String("proto", "", "file holding the output of spec proto")
	out := flags.String("out", "docs", "folder the pages go in")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *cli == "" || *descriptor == "" || *source == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: herdsman-docs --cli <file> --descriptor <file> --proto <file> [--out <folder>]")
		return 2
	}
	if err := generate(*cli, *descriptor, *source, *out); err != nil {
		fmt.Fprintf(stderr, "herdsman-docs: %v\n", err)
		return 1
	}
	return 0
}

func generate(cli, descriptor, source, out string) error {
	inputs := make([][]byte, 3)
	for i, path := range []string{cli, descriptor, source} {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		inputs[i] = data
	}
	commands, err := docgen.CommandPages(inputs[0])
	if err != nil {
		return err
	}
	config, err := docgen.ConfigPage(inputs[1], string(inputs[2]))
	if err != nil {
		return err
	}
	return docgen.Write(out, commands, config)
}
