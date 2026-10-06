package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func cmdSession(args []string) error {
	fs := flag.NewFlagSet("session", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: tessera session")
	}
	return session(os.Stdin, os.Stdout, os.Stderr, run, func(question string) error {
		return cmdAsk([]string{question})
	})
}

func session(in io.Reader, out, errs io.Writer, command func([]string) error, question func(string) error) error {
	fmt.Fprintln(out, "Tessera session. Enter a command or a question. Exit leaves the cluster running.")
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for {
		fmt.Fprint(out, "tessera> ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			return nil
		}
		first := strings.Fields(line)[0]
		var err error
		if sessionCommand(first) || first == "tessera" {
			var args []string
			args, err = commandWords(line)
			if err == nil {
				if args[0] == "tessera" {
					args = args[1:]
				}
				if len(args) == 0 || !sessionCommand(args[0]) {
					err = fmt.Errorf("enter a CLI command such as get apps, or a question")
				} else if args[0] == "up" || args[0] == "agent" || args[0] == "gateway" || args[0] == "controller" || args[0] == "watchdog" || args[0] == "mcp" || args[0] == "session" {
					err = fmt.Errorf("run tessera %s in a separate terminal", args[0])
				} else if sessionStdin(args) {
					err = fmt.Errorf("use a file path in the session; -f - reads the session input")
				} else {
					err = command(args)
				}
			}
		} else {
			err = question(line)
		}
		if err != nil {
			fmt.Fprintln(errs, "error:", err)
		}
	}
}

func sessionCommand(word string) bool {
	switch word {
	case "help", "-h", "--help", "version", "apply", "get", "confirm", "ask", "import", "backup", "restore", "cloud", "infra", "install", "drill", "up", "agent", "gateway", "controller", "watchdog", "mcp", "session":
		return true
	}
	return false
}

func sessionStdin(args []string) bool {
	for i, arg := range args {
		if arg == "-f=-" || arg == "--f=-" || ((arg == "-f" || arg == "--f") && i+1 < len(args) && args[i+1] == "-") {
			return true
		}
	}
	return false
}

func commandWords(line string) ([]string, error) {
	var words []string
	var b strings.Builder
	var quote rune
	escaped, started := false, false
	for _, r := range line {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped, started = true, true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote, started = r, true
		case ' ', '\t':
			if started {
				words = append(words, b.String())
				b.Reset()
				started = false
			}
		default:
			b.WriteRune(r)
			started = true
		}
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("unfinished quote or escape")
	}
	if started {
		words = append(words, b.String())
	}
	return words, nil
}
