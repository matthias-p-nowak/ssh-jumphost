package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// stdinReader is shared by all prompts so buffered input is not lost
// between questions.
var stdinReader = bufio.NewReader(os.Stdin)

// isTerminal reports whether stdin is an interactive terminal
// (`docker exec -it`), as opposed to a pipe or /dev/null.
func isTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// ask prints question (with def in brackets, if any) and returns the
// answer, or def for an empty answer.
func ask(question, def string) (string, error) {
	if def != "" {
		fmt.Printf("%s [%s]: ", question, def)
	} else {
		fmt.Printf("%s: ", question)
	}
	line, err := stdinReader.ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("no answer (input closed)")
	}
	if answer := strings.TrimSpace(line); answer != "" {
		return answer, nil
	}
	return def, nil
}

// askValid repeats ask until check accepts the answer.
func askValid(question, def string, check func(string) error) (string, error) {
	for {
		answer, err := ask(question, def)
		if err != nil {
			return "", err
		}
		if err := check(answer); err != nil {
			fmt.Println("  " + err.Error())
			continue
		}
		return answer, nil
	}
}
