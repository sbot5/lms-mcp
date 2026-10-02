package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sbot5/lms-mcp/internal/secrets"
	"golang.org/x/term"
)

const credentialTarget = "lms-mcp"

func runAuth(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: lms-mcp auth <ed|moodle|status>")
	}
	store, err := secrets.Open(credentialTarget)
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		return authStatus(store, out)
	case "ed":
		return authSet(store, secrets.EdToken, "ed", "Ed API token", in, out)
	case "moodle":
		return authMoodle(store, in, out)
	default:
		return fmt.Errorf("usage: lms-mcp auth <ed|moodle|status>")
	}
}

// authStatus prints which credentials are configured, never their values.
func authStatus(store secrets.Store, out io.Writer) error {
	fmt.Fprintf(out, "credential store: %s\n", store.Backend())
	rows := []struct {
		n        secrets.Name
		label    string
		provider string
	}{
		{secrets.EdToken, "Ed token", "ed"},
		{secrets.MoodleBase, "Moodle base URL", ""},
		{secrets.MoodleCookie, "Moodle session cookie", "moodle"},
		{secrets.MoodleICal, "Moodle calendar URL", ""},
	}
	for _, r := range rows {
		state := "not set"
		if _, ok, err := store.Get(r.n); err != nil {
			state = "error"
		} else if ok {
			state = "configured"
		} else if r.provider != "" && secrets.Proxy(r.provider) {
			state = "via proxy"
		}
		fmt.Fprintf(out, "  %-22s %s\n", r.label, state)
	}
	return nil
}

// authSet stores one secret read from the terminal (hidden) or a pipe.
func authSet(store secrets.Store, n secrets.Name, provider, label string, in io.Reader, out io.Writer) error {
	if store.Backend() == "env" {
		return fmt.Errorf("this environment uses read-only credentials; set the %s via an environment variable instead (see docs/plan.md §8)", label)
	}
	value, err := readSecret(in, out, fmt.Sprintf("Paste the %s (input hidden): ", label))
	if err != nil {
		return err
	}
	if value == "" {
		return fmt.Errorf("no %s entered", label)
	}
	if err := store.Set(n, value); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s stored.\n", label)
	return nil
}

func authMoodle(store secrets.Store, in io.Reader, out io.Writer) error {
	if store.Backend() == "env" {
		fmt.Fprintln(out, "This environment reads Moodle credentials from the environment:")
		fmt.Fprintln(out, "  MOODLE_BASE_URL, MOODLE_COOKIE (or MOODLE_AUTH=proxy), MOODLE_ICAL_URL.")
		fmt.Fprintln(out, "Interactive browser login is available on Windows in a later release.")
		return nil
	}
	// On Windows in M1, accept a pasted cookie and the calendar URL. The
	// dedicated Edge login flow lands in M3.
	if err := authSet(store, secrets.MoodleCookie, "moodle", "Moodle session cookie (name=value)", in, out); err != nil {
		return err
	}
	fmt.Fprintln(out, "Optional: paste the calendar export URL, or leave blank to skip.")
	ical, err := readSecret(in, out, "Calendar URL (input hidden): ")
	if err != nil {
		return err
	}
	if ical != "" {
		if err := store.Set(secrets.MoodleICal, ical); err != nil {
			return err
		}
		fmt.Fprintln(out, "Calendar URL stored.")
	}
	return nil
}

// readSecret reads one secret. If in is a terminal it disables echo; otherwise
// it reads a single line (for piped input in scripts and tests).
func readSecret(in io.Reader, out io.Writer, prompt string) (string, error) {
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(out, prompt)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(out)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	r := bufio.NewReader(in)
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
