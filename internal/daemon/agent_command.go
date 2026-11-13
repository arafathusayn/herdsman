package daemon

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var environmentPrefix = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// splitAgentCommand reads a command line the way a shell splits words: single
// and double quotes and backslashes. herdr runs the words with no shell, so a
// leading NAME=value, which only a shell understands, is refused.
func splitAgentCommand(line string) ([]string, error) {
	var words []string
	var word strings.Builder
	inWord := false
	quote := rune(0)
	escaped := false
	for _, char := range line {
		switch {
		case escaped:
			word.WriteRune(char)
			escaped = false
		case quote == '\'':
			if char == '\'' {
				quote = 0
			} else {
				word.WriteRune(char)
			}
		case char == '\\':
			escaped, inWord = true, true
		case quote == '"':
			if char == '"' {
				quote = 0
			} else {
				word.WriteRune(char)
			}
		case char == '\'' || char == '"':
			quote, inWord = char, true
		case char == ' ' || char == '\t' || char == '\n':
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		default:
			word.WriteRune(char)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("the agent command has an unfinished %c quote", quote)
	}
	if escaped {
		return nil, errors.New("the agent command ends in a backslash")
	}
	if inWord {
		words = append(words, word.String())
	}
	if len(words) > 0 && environmentPrefix.MatchString(words[0]) {
		return nil, fmt.Errorf("the agent command starts with %s; herdr starts it without a shell, so set that in the environment instead", words[0])
	}
	return words, nil
}
