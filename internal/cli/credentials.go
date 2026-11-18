package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"golang.org/x/term"

	"gitlab.com/system4us/issue-timeline/internal/credentials"
)

// usage is the text of `issue-timeline credentials` with no valid command.
func usage() string {
	var names []string
	for _, s := range credentials.Specs() {
		names = append(names, s.Name+" ("+s.Help+")")
	}
	return `usage:
  issue-timeline credentials set <name> [host]   type the value (hidden), or pipe it on stdin
  issue-timeline credentials list                which secrets are set, and where (never the values)
  issue-timeline credentials clear <name> [host]

names: ` + strings.Join(names, "\n       ") + `

host:  the URL of the Redmine or Jira site the secret is for (https://redmine.example.com). Without it, the secret is
       bound to the first site it is used with, and never sent to another one.`
}

// CredentialsCommand runs `issue-timeline credentials <set|list|clear> [name] [host]` against st, and returns the exit
// code. A value is typed in hidden, or piped on stdin; it is never an argument. Output goes to stdout, errors to stderr.
func CredentialsCommand(argv []string, st *credentials.Store, stdin *os.File, stdout, stderr io.Writer) int {
	var verb, base, host string
	if len(argv) > 0 {
		verb = argv[0]
	}
	if len(argv) > 1 {
		base = argv[1]
	}
	if len(argv) > 2 {
		host = argv[2]
	}
	name := base
	if base != "" && host != "" {
		name = base + "@" + host
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	switch {
	case verb == "set" && name != "":
		value, err := readHidden(name+": ", stdin, stdout)
		if err != nil {
			return fail(err)
		}
		where, err := st.Set(name, value)
		if err != nil {
			return fail(err)
		}
		place := "owner-only file in the data folder"
		if where == "keyring" {
			place = "OS keyring"
		}
		fmt.Fprintf(stdout, "saved %s in the %s\n", name, place)
		return 0
	case verb == "list":
		list, err := st.List()
		if err != nil {
			return fail(err)
		}
		bound, err := st.BoundOrigins()
		if err != nil {
			return fail(err)
		}
		for _, n := range orderNames(list) {
			where := list[n]
			if where == "" {
				fmt.Fprintf(stdout, "%s: not set\n", n)
				continue
			}
			suffix := ""
			if bound[n] != "" {
				suffix = " (for " + bound[n] + ")"
			}
			fmt.Fprintf(stdout, "%s: %s%s\n", n, where, suffix)
		}
		return 0
	case verb == "clear" && name != "":
		if err := st.Clear(name); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "cleared %s\n", name)
		return 0
	}
	fmt.Fprintf(stdout, "%s\n", usage())
	if verb != "" {
		return 1
	}
	return 0
}

// orderNames lists the known secrets first, in their order, then any per-site ones, sorted.
func orderNames(list map[string]string) []string {
	var out, rest []string
	known := map[string]bool{}
	for _, n := range credentials.KnownNames() {
		known[n] = true
		out = append(out, n)
	}
	for n := range list {
		if !known[n] {
			rest = append(rest, n)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// readHidden reads one line: typed in hidden when stdin is a terminal (after prompt), otherwise the first line of the
// pipe. Ctrl-C cancels.
func readHidden(prompt string, stdin *os.File, stdout io.Writer) (string, error) {
	if stdin == nil {
		return "", nil
	}
	fd := int(stdin.Fd())
	if !term.IsTerminal(fd) {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("reading the value: %w", err)
		}
		line, _, _ := strings.Cut(string(b), "\n")
		return strings.TrimSpace(line), nil
	}

	fmt.Fprint(stdout, prompt)
	old, err := term.MakeRaw(fd)
	if err != nil {
		return "", err
	}
	defer term.Restore(fd, old)

	var buf []byte
	one := make([]byte, 1)
	for {
		n, err := stdin.Read(one)
		if err != nil {
			return "", fmt.Errorf("reading the value: %w", err)
		}
		if n == 0 {
			continue
		}
		switch c := one[0]; c {
		case '\r', '\n':
			fmt.Fprintln(stdout)
			return strings.TrimSpace(string(buf)), nil
		case 3: // Ctrl-C: raw mode does not turn it into a signal
			return "", errors.New("cancelled")
		case 8, 127: // backspace
			buf = dropLastRune(buf)
		default:
			buf = append(buf, c)
		}
	}
}

// dropLastRune removes the last character, which may be several bytes of UTF-8.
func dropLastRune(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	i := len(b) - 1
	for i > 0 && b[i]&0xC0 == 0x80 {
		i--
	}
	return b[:i]
}
