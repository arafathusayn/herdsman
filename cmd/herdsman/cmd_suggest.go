package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

// suggestCall serves the catalog verbs. suggestRun applies a recipe; it is
// separate because no service method may do that, and main.go sets it.
var (
	suggestCall = sdkSuggestCall
	suggestRun  func(ctx context.Context, dir, id string, in io.Reader, out, errOut io.Writer, isTerminal bool) error
	// suggestIsTerminal must ask the kernel: a recipe depends on the answer.
	suggestIsTerminal func(stdin any) bool
)

// maxBodyFile bounds what add reads; the catalog refuses a smaller entry anyway.
const maxBodyFile = 1 << 20

const suggestHelp = `usage: herdsman suggest [<query>...] [--kind tip|mcp|skill|plugin|mod|repo]
                        [--for <harness>] [--tag <tag>] [--json]
       herdsman suggest show <id> [--json]
       herdsman suggest add --title <text> --tag <tag>... --body-file <path> [--id <id>]
                        [--kind <kind>] [--url <url>] [--repo <url>] [--project] [--json]
       herdsman suggest apply <id> [--run] [--json]
       herdsman suggest source add <url> [--ref <commit|tag>] [--json]
       herdsman suggest source list [--json]
       herdsman suggest source remove <url>
       herdsman suggest source update <url> [--ref <commit|tag>] [--yes] [--json]

Searches the catalog, ranked for this machine and project. apply prints a
recipe; --run executes it, on a terminal only, after approving its checksum.
source update shows the changes before it moves the pin.

exit codes:
  0  done
  1  the entry or source was not found, or a recipe was refused or failed
  2  bad usage
`

func init() {
	register(command{name: "suggest", summary: "Search the catalog of tips and tools.", help: suggestHelp, run: runSuggestCommand})
	register(command{name: "recommend", summary: "The same as suggest.", help: suggestHelp, run: runSuggestCommand})
}

// callSuggest sends one request and decodes the response into result.
func callSuggest(method string, request, result any) ([]byte, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	response, err := suggestCall(context.Background(), method, data)
	if err != nil {
		return nil, err
	}
	if result != nil {
		if err := json.Unmarshal(response, result); err != nil {
			return nil, err
		}
	}
	return response, nil
}

func runSuggestCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "show":
			return runSuggestShow(args[1:], stdout, stderr)
		case "add":
			return runSuggestAdd(args[1:], stdout, stderr)
		case "apply":
			return runSuggestApply(args[1:], stdin, stdout, stderr)
		case "source":
			return runSuggestSource(args[1:], stdin, stdout, stderr)
		}
	}
	return runSuggestSearch(args, stdout, stderr)
}

type suggestSource struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Commit string `json:"commit"`
}

func (s suggestSource) String() string {
	if s.Commit != "" {
		return s.Kind + " " + s.Name + "@" + s.Commit
	}
	return s.Kind
}

type suggestProblem struct {
	Source suggestSource `json:"source"`
	File   string        `json:"file"`
	Err    string        `json:"error"`
}

// warnProblems quotes the text, which may come from a remote catalog.
func warnProblems(stderr io.Writer, problems []suggestProblem) {
	for _, p := range problems {
		fmt.Fprintf(stderr, "herdsman: warning: %s: %q: %q\n", p.Source, p.File, p.Err)
	}
}

func runSuggestSearch(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("suggest", stderr)
	var request struct {
		Dir  string `json:"dir"`
		Text string `json:"text"`
		Kind string `json:"kind"`
		For  string `json:"for"`
		Tag  string `json:"tag"`
	}
	fs.StringVar(&request.Kind, "kind", "", "only this kind of entry")
	fs.StringVar(&request.For, "for", "", "rank for this harness instead of the detected ones")
	fs.StringVar(&request.Tag, "tag", "", "only entries with this tag")
	asJSON := fs.Bool("json", false, "print the ranking with its reasons as JSON")
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	request.Text = strings.Join(positional, " ")
	if request.Dir, err = workingFolder(); err != nil {
		return fail(stderr, err)
	}
	var result struct {
		Items []struct {
			Entry struct {
				ID     string        `json:"id"`
				Kind   string        `json:"kind"`
				Title  string        `json:"title"`
				Source suggestSource `json:"source"`
			} `json:"entry"`
			Score   int `json:"score"`
			Reasons []struct {
				Code  string `json:"code"`
				Value string `json:"value"`
			} `json:"reasons"`
		} `json:"items"`
		Problems []suggestProblem `json:"problems"`
	}
	data, err := callSuggest("Search", request, &result)
	if err != nil {
		return fail(stderr, err)
	}
	warnProblems(stderr, result.Problems)
	if *asJSON {
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	if len(result.Items) == 0 {
		fmt.Fprintln(stdout, "no suggestions")
		return 0
	}
	for _, item := range result.Items {
		why := make([]string, 0, len(item.Reasons))
		for _, reason := range item.Reasons {
			why = append(why, strings.TrimSuffix(reason.Code+"="+reason.Value, "="))
		}
		fmt.Fprintf(stdout, "%-28s %-7s %4d  %s  [%s] (%s)\n", item.Entry.ID, item.Entry.Kind, item.Score, item.Entry.Title, item.Entry.Source, strings.Join(why, ", "))
	}
	return 0
}

func runSuggestShow(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("suggest show", stderr)
	asJSON := fs.Bool("json", false, "print the entry as JSON")
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) != 1 {
		return usageError(stderr, "suggest show takes one id")
	}
	dir, err := workingFolder()
	if err != nil {
		return fail(stderr, err)
	}
	var result struct {
		Entry struct {
			ID     string        `json:"id"`
			Kind   string        `json:"kind"`
			Title  string        `json:"title"`
			URL    string        `json:"url"`
			Repo   string        `json:"repo"`
			Tags   []string      `json:"tags"`
			Body   string        `json:"body"`
			Source suggestSource `json:"source"`
		} `json:"entry"`
	}
	data, err := callSuggest("Get", map[string]string{"dir": dir, "id": positional[0]}, &result)
	if err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	e := result.Entry
	fmt.Fprintf(stdout, "%s (%s)\n%s\nsource: %s\ntags: %s\n", e.Title, e.Kind, e.ID, e.Source, strings.Join(e.Tags, ", "))
	for _, link := range []string{e.URL, e.Repo} {
		if link != "" {
			fmt.Fprintf(stdout, "link: %s\n", link)
		}
	}
	fmt.Fprintf(stdout, "\n%s\n", e.Body)
	return 0
}

// repeatedFlag collects every use of a flag.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ",") }

func (r *repeatedFlag) Set(value string) error {
	*r = append(*r, value)
	return nil
}

func readBodyFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBodyFile))
	return string(data), err
}

func runSuggestAdd(args []string, stdout, stderr io.Writer) int {
	fs := verbFlags("suggest add", stderr)
	var request struct {
		Dir     string   `json:"dir"`
		Project bool     `json:"project"`
		ID      string   `json:"id"`
		Kind    string   `json:"kind"`
		Title   string   `json:"title"`
		Tags    []string `json:"tags"`
		URL     string   `json:"url"`
		Repo    string   `json:"repo"`
		Body    string   `json:"body"`
	}
	var tags repeatedFlag
	fs.StringVar(&request.ID, "id", "", "the entry's id (default: from the title)")
	fs.StringVar(&request.Kind, "kind", "tip", "the kind of entry")
	fs.StringVar(&request.Title, "title", "", "the entry's title")
	fs.StringVar(&request.URL, "url", "", "a link")
	fs.StringVar(&request.Repo, "repo", "", "a repository link")
	fs.BoolVar(&request.Project, "project", false, "write to the project's context repo")
	fs.Var(&tags, "tag", "a tag; repeat for more")
	bodyFile := fs.String("body-file", "", "file holding the entry's Markdown")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) != 0 {
		return usageError(stderr, "suggest add takes only flags")
	}
	request.Tags = tags
	if request.Body, err = readBodyFile(*bodyFile); err != nil {
		return fail(stderr, err)
	}
	if request.Dir, err = workingFolder(); err != nil {
		return fail(stderr, err)
	}
	var result struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}
	data, err := callSuggest("Add", request, &result)
	if err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	fmt.Fprintf(stdout, "added %s\npath: %s\n", result.ID, result.Path)
	return 0
}

func runSuggestApply(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := verbFlags("suggest apply", stderr)
	runIt := fs.Bool("run", false, "execute the recipe, on a terminal, after approval")
	asJSON := fs.Bool("json", false, "print the recipe as JSON")
	positional, _, err := parseVerb(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) != 1 {
		return usageError(stderr, "suggest apply takes one id")
	}
	if *runIt && *asJSON {
		return usageError(stderr, "--run talks to a person, so it cannot print JSON")
	}
	dir, err := workingFolder()
	if err != nil {
		return fail(stderr, err)
	}
	if *runIt {
		if err := suggestRun(context.Background(), dir, positional[0], stdin, stdout, stderr, suggestIsTerminal(stdin)); err != nil {
			return fail(stderr, err)
		}
		return 0
	}
	var result struct {
		Recipe struct {
			ID       string        `json:"id"`
			Title    string        `json:"title"`
			Source   suggestSource `json:"source"`
			Kind     string        `json:"kind"`
			Text     string        `json:"text"`
			Touches  []string      `json:"touches"`
			Checksum string        `json:"checksum"`
		} `json:"recipe"`
		Approved bool `json:"approved"`
	}
	data, err := callSuggest("Plan", map[string]string{"dir": dir, "id": positional[0]}, &result)
	if err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	state := "needs approval"
	if result.Approved {
		state = "approved"
	}
	r := result.Recipe
	fmt.Fprintf(stdout, "%s: %s\nsource: %s\n%s: %s\ntouches: %s\nchecksum: %s (%s)\n", r.ID, r.Title, r.Source, r.Kind, r.Text, strings.Join(r.Touches, ", "), r.Checksum, state)
	return 0
}

func runSuggestSource(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return usageError(stderr, "suggest source takes add, list, remove or update")
	}
	fs := verbFlags("suggest source "+args[0], stderr)
	ref := fs.String("ref", "", "a commit or tag (default: the remote's HEAD)")
	yes := fs.Bool("yes", false, "move the pin without asking")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	positional, _, err := parseVerb(fs, args[1:])
	if err != nil {
		return 2
	}
	wanted := 1
	if args[0] == "list" {
		wanted = 0
	}
	if !slices.Contains([]string{"add", "list", "remove", "update"}, args[0]) {
		return usageError(stderr, "suggest source takes add, list, remove or update")
	}
	if len(positional) != wanted {
		return usageError(stderr, "wrong number of arguments for suggest source "+args[0])
	}
	switch args[0] {
	case "list":
		return listSuggestSources(*asJSON, stdout, stderr)
	case "add":
		data, err := callSuggest("SourceAdd", map[string]string{"url": positional[0], "ref": *ref}, nil)
		return printSourceResult(data, err, *asJSON, "added", stdout, stderr)
	case "remove":
		data, err := callSuggest("SourceRemove", map[string]string{"url": positional[0]}, nil)
		return printSourceResult(data, err, *asJSON, "removed", stdout, stderr)
	}
	return updateSuggestSource(positional[0], *ref, *yes, *asJSON, stdin, stdout, stderr)
}

func printSourceResult(data []byte, err error, asJSON bool, verb string, stdout, stderr io.Writer) int {
	if err != nil {
		return fail(stderr, err)
	}
	if asJSON {
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	fmt.Fprintf(stdout, "%s: %s\n", verb, data)
	return 0
}

func listSuggestSources(asJSON bool, stdout, stderr io.Writer) int {
	var result struct {
		Sources []struct {
			URL    string `json:"url"`
			Commit string `json:"commit"`
		} `json:"sources"`
	}
	dir, err := workingFolder()
	if err != nil {
		return fail(stderr, err)
	}
	data, err := callSuggest("Sources", map[string]string{"dir": dir}, &result)
	if err != nil {
		return fail(stderr, err)
	}
	if asJSON {
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	if len(result.Sources) == 0 {
		fmt.Fprintln(stdout, "no remote sources")
	}
	for _, source := range result.Sources {
		fmt.Fprintf(stdout, "%s@%s\n", source.URL, source.Commit)
	}
	return 0
}

type suggestPin struct {
	URL    string `json:"url"`
	Commit string `json:"commit"`
}

// updateSuggestSource shows what would change and moves the pin only after
// --yes or a typed yes, so the diff is always what was accepted.
func updateSuggestSource(url, ref string, yes, asJSON bool, stdin io.Reader, stdout, stderr io.Writer) int {
	var preview struct {
		From    suggestPin `json:"from"`
		To      suggestPin `json:"to"`
		Changes []struct {
			ID     string `json:"id"`
			Action string `json:"action"`
			Diff   string `json:"diff"`
		} `json:"changes"`
		Problems []suggestProblem `json:"problems"`
	}
	data, err := callSuggest("SourcePreview", map[string]string{"url": url, "ref": ref}, &preview)
	if err != nil {
		return fail(stderr, err)
	}
	warnProblems(stderr, preview.Problems)
	if asJSON {
		fmt.Fprintln(stdout, string(data))
	} else {
		for _, change := range preview.Changes {
			fmt.Fprintf(stdout, "%s %s\n%s\n", change.Action, change.ID, change.Diff)
		}
	}
	if preview.From.Commit == preview.To.Commit {
		if !asJSON {
			fmt.Fprintf(stdout, "%s is already at %s\n", url, preview.To.Commit)
		}
		return 0
	}
	if !yes && !confirmPinMove(preview.To.Commit, stdin, stdout, asJSON) {
		fmt.Fprintf(stderr, "herdsman: the pin did not move; it stays at %s\n", preview.From.Commit)
		return 0
	}
	_, err = callSuggest("SourcePin", map[string]string{"url": url, "from": preview.From.Commit, "to": preview.To.Commit}, nil)
	if err != nil {
		return fail(stderr, err)
	}
	if !asJSON {
		fmt.Fprintf(stdout, "%s moved to %s\n", url, preview.To.Commit)
	}
	return 0
}

func confirmPinMove(commit string, stdin io.Reader, stdout io.Writer, asJSON bool) bool {
	if asJSON || !suggestIsTerminal(stdin) {
		return false
	}
	fmt.Fprintf(stdout, "Move the pin to %s? Type yes: ", commit)
	line, err := bufio.NewReader(stdin).ReadString('\n')
	return err == nil && strings.TrimSpace(line) == "yes"
}
