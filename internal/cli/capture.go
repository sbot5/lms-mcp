package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sbot5/lms-mcp/internal/anonymize"
	"github.com/sbot5/lms-mcp/internal/config"
	"github.com/sbot5/lms-mcp/internal/ed"
	"github.com/sbot5/lms-mcp/internal/secrets"
	"github.com/sbot5/lms-mcp/internal/syncer"
)

type captureOptions struct {
	kind     string
	id       int
	courseID int
	output   string
}

const captureKinds = "whoami, lessons, lesson, resources, questions, responses, challenge, submissions, attempt, mark, threads, thread"

func runCapture(ctx context.Context, args []string, out, errOut io.Writer) error {
	if len(args) == 0 || args[0] != "ed" {
		return errors.New("capture: use `capture ed -kind <kind> -out <fixture.json>`")
	}
	fs := flag.NewFlagSet("capture ed", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var options captureOptions
	configPath := fs.String("config", "", "configuration JSON path")
	fs.StringVar(&options.kind, "kind", "", "one of: "+captureKinds)
	fs.IntVar(&options.id, "id", 0, "lesson, slide, challenge, mark or thread ID")
	fs.IntVar(&options.courseID, "course-id", 0, "course ID for list endpoints")
	fs.StringVar(&options.output, "out", "", "destination for sanitized JSON only")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("capture: unexpected positional arguments")
	}
	if err := options.validate(); err != nil {
		return err
	}
	path := *configPath
	if path == "" {
		path = defaultConfigPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	sec, err := secrets.Open(credentialTarget)
	if err != nil {
		return errors.New("capture: could not open credential backend")
	}
	p, err := syncer.Build(cfg, sec)
	if err != nil || p.Ed == nil {
		return errors.New("capture: Ed is unavailable; configure Ed and its credential first")
	}
	if err := captureEd(ctx, p.Ed, options); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "Saved sanitized Ed fixture.")
	return err
}

func (o captureOptions) validate() error {
	if o.output == "" || filepath.Ext(o.output) != ".json" {
		return errors.New("capture: -out must name a JSON file")
	}
	if o.id < 0 || o.courseID < 0 {
		return errors.New("capture: IDs must be positive integers")
	}
	switch o.kind {
	case "whoami":
		if o.id != 0 || o.courseID != 0 {
			return errors.New("capture: whoami does not accept IDs")
		}
	case "lessons", "resources", "threads":
		if o.courseID <= 0 || o.id != 0 {
			return errors.New("capture: this list kind requires -course-id only")
		}
	case "lesson", "questions", "responses", "challenge", "submissions", "attempt", "mark", "thread":
		if o.id <= 0 || o.courseID != 0 {
			return errors.New("capture: this kind requires -id only")
		}
	default:
		return errors.New("capture: unsupported Ed kind; see `capture ed -h`")
	}
	return nil
}

// captureEd keeps the complete API envelope for fixture fidelity, but every
// route is constructed here from an enum and integer IDs. No endpoint, token
// or alternate identity is accepted from a command-line argument.
func captureEd(ctx context.Context, client *ed.Client, options captureOptions) error {
	if err := options.validate(); err != nil {
		return err
	}
	var endpoint string
	switch options.kind {
	case "whoami":
		endpoint = "/user"
	case "lessons", "resources":
		endpoint = fmt.Sprintf("/courses/%d/%s", options.courseID, options.kind)
	case "threads":
		// One API page is a fixture, not an unbounded course-data dump.
		endpoint = fmt.Sprintf("/courses/%d/threads?limit=100&offset=0&sort=new", options.courseID)
	case "lesson":
		endpoint = fmt.Sprintf("/lessons/%d", options.id)
	case "questions":
		endpoint = fmt.Sprintf("/lessons/slides/%d/questions", options.id)
	case "responses":
		endpoint = fmt.Sprintf("/lessons/slides/%d/questions/responses", options.id)
	case "challenge":
		endpoint = fmt.Sprintf("/challenges/%d", options.id)
	case "mark":
		endpoint = fmt.Sprintf("/lesson_marks/%d?rubric_items=true", options.id)
	case "thread":
		endpoint = fmt.Sprintf("/threads/%d", options.id)
	case "submissions", "attempt":
		who, err := client.Whoami(ctx)
		if err != nil {
			return err
		}
		if who.UserID <= 0 {
			return errors.New("capture: Ed did not return the signed-in user's ID")
		}
		if options.kind == "submissions" {
			endpoint = fmt.Sprintf("/users/%d/challenges/%d/submissions", who.UserID, options.id)
		} else {
			endpoint = fmt.Sprintf("/lessons/%d/attempts/%d", options.id, who.UserID)
		}
	}
	var raw json.RawMessage
	if err := client.Get(ctx, endpoint, &raw); err != nil {
		return err
	}
	sanitized, err := anonymize.New().JSON(raw)
	if err != nil {
		return err
	}
	return writeCapture(options.output, sanitized)
}

func writeCapture(destination string, sanitized []byte) error {
	dir := filepath.Dir(destination)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errors.New("capture: could not create output directory")
	}
	f, err := os.CreateTemp(dir, ".capture-*.tmp")
	if err != nil {
		return errors.New("capture: could not create sanitized fixture")
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(sanitized); err != nil {
		_ = f.Close()
		return errors.New("capture: could not write sanitized fixture")
	}
	if err := f.Close(); err != nil {
		return errors.New("capture: could not close sanitized fixture")
	}
	if err := os.Rename(f.Name(), destination); err != nil {
		return errors.New("capture: could not save sanitized fixture")
	}
	return nil
}
