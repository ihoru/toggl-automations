package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/ihoru/toggl-automations/internal/credentials"
	"github.com/ihoru/toggl-automations/internal/rewrite"
	"github.com/ihoru/toggl-automations/internal/toggl"
)

type Engine interface {
	Run(context.Context, rewrite.Request) (rewrite.Result, error)
}

type EngineFactory func(token string) Engine

type Getenv func(string) string

type CredentialStore interface {
	Load() (string, credentials.Source, error)
	Save(string) (credentials.Source, error)
	Delete() (bool, error)
}

type ReadSecret func(io.Writer) (string, error)

type Dependencies struct {
	Getenv      Getenv
	Credentials CredentialStore
	ReadSecret  ReadSecret
	Factory     EngineFactory
}

func Run(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	dependencies Dependencies,
) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		printUsage(stdout)
		return 0
	}
	if len(args) >= 1 && args[0] == "auth" {
		return runAuth(args[1:], stdout, stderr, dependencies)
	}
	if len(args) < 2 || args[0] != "entries" || args[1] != "rewrite" {
		printUsage(stderr)
		fmt.Fprintln(stderr, "error: expected subcommand: auth or entries rewrite")
		return 2
	}
	if len(args) == 3 && isHelp(args[2]) {
		printRewriteUsage(stdout)
		return 0
	}

	flags := flag.NewFlagSet("entries rewrite", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { printRewriteUsage(stderr) }
	description := flags.String("description", "", "exact source description")
	project := flags.String("project", "", "exact source project name or id:<number>")
	var newDescription optionalString
	var newProject optionalString
	flags.Var(&newDescription, "new-description", "replacement description")
	flags.Var(&newProject, "new-project", "replacement project name or id:<number>")
	apply := flags.Bool("apply", false, "apply changes instead of previewing them")

	if err := flags.Parse(args[2:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "error: unexpected arguments: %s\n", strings.Join(flags.Args(), " "))
		return 2
	}

	request := rewrite.Request{
		Description:    *description,
		Project:        *project,
		NewDescription: newDescription.Pointer(),
		NewProject:     newProject.Pointer(),
		Apply:          *apply,
	}
	if err := validateParsedRequest(request); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	token, err := resolveToken(dependencies)
	if errors.Is(err, credentials.ErrNotFound) {
		fmt.Fprintln(stderr, "error: Toggl API token is not configured; run 'toggl-automations auth login' or set TOGGL_API_TOKEN")
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: load Toggl API token: %v\n", err)
		return 1
	}
	factory := dependencies.Factory
	if factory == nil {
		factory = func(token string) Engine {
			return rewrite.NewService(toggl.NewClient(token))
		}
	}

	result, err := factory(token).Run(ctx, request)
	if hasPrintableResult(result) {
		printResult(stdout, request, result)
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func runAuth(args []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help")) {
		printAuthUsage(stdout)
		return 0
	}

	switch args[0] {
	case "login":
		return runAuthLogin(args[1:], stdout, stderr, dependencies)
	case "status":
		if len(args) == 2 && isHelp(args[1]) {
			fmt.Fprintln(stdout, "Usage: toggl-automations auth status")
			return 0
		}
		if len(args) != 1 {
			fmt.Fprintln(stderr, "error: auth status does not accept arguments")
			return 2
		}
		return runAuthStatus(stdout, stderr, dependencies)
	case "logout":
		if len(args) == 2 && isHelp(args[1]) {
			fmt.Fprintln(stdout, "Usage: toggl-automations auth logout")
			return 0
		}
		if len(args) != 1 {
			fmt.Fprintln(stderr, "error: auth logout does not accept arguments")
			return 2
		}
		return runAuthLogout(stdout, stderr, dependencies)
	default:
		printAuthUsage(stderr)
		fmt.Fprintf(stderr, "error: unknown auth command %q\n", args[0])
		return 2
	}
}

func runAuthLogin(args []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprintln(stdout, "Usage: toggl-automations auth login [--from-env]")
		return 0
	}
	fromEnvironment := false
	if len(args) == 1 && args[0] == "--from-env" {
		fromEnvironment = true
	} else if len(args) != 0 {
		fmt.Fprintln(stderr, "error: usage: toggl-automations auth login [--from-env]")
		return 2
	}
	if dependencies.Credentials == nil {
		fmt.Fprintln(stderr, "error: credential storage is unavailable")
		return 1
	}

	var token string
	if fromEnvironment {
		token = getenv(dependencies, "TOGGL_API_TOKEN")
		if token == "" {
			fmt.Fprintln(stderr, "error: TOGGL_API_TOKEN is not set")
			return 2
		}
	} else {
		if dependencies.ReadSecret == nil {
			fmt.Fprintln(stderr, "error: secure token input is unavailable; use --from-env")
			return 1
		}
		var err error
		token, err = dependencies.ReadSecret(stderr)
		if err != nil {
			fmt.Fprintf(stderr, "error: read Toggl API token: %v\n", err)
			return 1
		}
	}

	source, err := dependencies.Credentials.Save(token)
	if err != nil {
		fmt.Fprintf(stderr, "error: save Toggl API token: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Token saved in %s.\n", source)
	if !fromEnvironment && getenv(dependencies, "TOGGL_API_TOKEN") != "" {
		fmt.Fprintln(stdout, "Warning: TOGGL_API_TOKEN is set and overrides the saved token.")
	}
	return 0
}

func isHelp(argument string) bool {
	return argument == "-h" || argument == "--help" || argument == "help"
}

func runAuthStatus(stdout, stderr io.Writer, dependencies Dependencies) int {
	token, source, err := resolveTokenWithSource(dependencies)
	if errors.Is(err, credentials.ErrNotFound) {
		fmt.Fprintln(stdout, "Token configured: no")
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: load Toggl API token: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Token configured: yes (%s, %s)\n", source, maskToken(token))
	return 0
}

func runAuthLogout(stdout, stderr io.Writer, dependencies Dependencies) int {
	if dependencies.Credentials == nil {
		fmt.Fprintln(stderr, "error: credential storage is unavailable")
		return 1
	}
	removed, err := dependencies.Credentials.Delete()
	if err != nil {
		fmt.Fprintf(stderr, "error: delete stored Toggl API token: %v\n", err)
		return 1
	}
	if removed {
		fmt.Fprintln(stdout, "Stored token removed.")
	} else {
		fmt.Fprintln(stdout, "No stored token found.")
	}
	if getenv(dependencies, "TOGGL_API_TOKEN") != "" {
		fmt.Fprintln(stdout, "TOGGL_API_TOKEN is still set and continues to override stored credentials.")
	}
	return 0
}

func resolveToken(dependencies Dependencies) (string, error) {
	token, _, err := resolveTokenWithSource(dependencies)
	return token, err
}

func resolveTokenWithSource(dependencies Dependencies) (string, credentials.Source, error) {
	if token := strings.TrimSpace(getenv(dependencies, "TOGGL_API_TOKEN")); token != "" {
		return token, credentials.SourceEnvironment, nil
	}
	if dependencies.Credentials == nil {
		return "", "", credentials.ErrNotFound
	}
	return dependencies.Credentials.Load()
}

func getenv(dependencies Dependencies, name string) string {
	if dependencies.Getenv == nil {
		return ""
	}
	return dependencies.Getenv(name)
}

func maskToken(token string) string {
	if len(token) <= 4 {
		return "****"
	}
	return "..." + token[len(token)-4:]
}

type optionalString struct {
	value string
	set   bool
}

func (value *optionalString) String() string {
	return value.value
}

func (value *optionalString) Set(input string) error {
	value.value = input
	value.set = true
	return nil
}

func (value *optionalString) Pointer() *string {
	if !value.set {
		return nil
	}
	result := value.value
	return &result
}

func validateParsedRequest(request rewrite.Request) error {
	if request.Description == "" {
		return errors.New("--description is required and must not be empty")
	}
	if request.Project == "" {
		return errors.New("--project is required and must not be empty")
	}
	if request.NewDescription != nil && *request.NewDescription == "" {
		return errors.New("--new-description must not be empty")
	}
	if request.NewProject != nil && *request.NewProject == "" {
		return errors.New("--new-project must not be empty")
	}
	if request.Apply && !request.HasChanges() {
		return errors.New("--apply requires --new-description, --new-project, or both")
	}
	return nil
}

func hasPrintableResult(result rewrite.Result) bool {
	return result.Ready
}

func printResult(output io.Writer, request rewrite.Request, result rewrite.Result) {
	fmt.Fprintf(output, "Workspace: %d\n", result.SourceProject.Workspace())
	fmt.Fprintf(output, "Source project: %s (id:%d)\n", result.SourceProject.Name, result.SourceProject.ID)
	fmt.Fprintf(output, "Timezone: %s\n", result.Timezone)
	if result.TimezoneWarning != "" {
		fmt.Fprintf(output, "Warning: %s\n", result.TimezoneWarning)
	}
	fmt.Fprintf(output, "Matches: %d\n", len(result.Matches))
	fmt.Fprintf(output, "Skipped running: %d\n", result.SkippedRunning)

	if request.HasChanges() {
		fmt.Fprintf(output, "Would change: %d\n", len(result.Changes))
		if result.Applied {
			fmt.Fprintf(output, "Succeeded: %d\n", len(result.Succeeded))
			fmt.Fprintf(output, "Failed: %d\n", len(result.Failures))
		}
		printChanges(output, result.Changes)
	} else {
		printEntries(output, result.Matches)
	}

	if len(result.Failures) != 0 {
		fmt.Fprintln(output, "Failures:")
		for _, failure := range result.Failures {
			fmt.Fprintf(output, "  %d: %s\n", failure.ID, failure.Message)
		}
	}
}

func printEntries(output io.Writer, entries []rewrite.Entry) {
	latest := latestEntries(entries, 10)
	fmt.Fprintf(output, "Latest entries shown: %d\n", len(latest))
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	for _, entry := range latest {
		fmt.Fprintf(
			writer,
			"%d\t%s -> %s\t%s\t%q\t%s\n",
			entry.ID,
			entry.Start.Format("2006-01-02 15:04:05 -07:00"),
			entry.Stop.Format("2006-01-02 15:04:05 -07:00"),
			entry.Duration,
			entry.Description,
			entry.ProjectName,
		)
	}
	writer.Flush()
}

func printChanges(output io.Writer, changes []rewrite.Change) {
	latest := latestChanges(changes, 10)
	fmt.Fprintf(output, "Latest changes shown: %d\n", len(latest))
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	for _, change := range latest {
		fmt.Fprintf(
			writer,
			"%d\t%s -> %s\t%s\tdescription %q -> %q\tproject %s (id:%d) -> %s (id:%d)\n",
			change.ID,
			change.Start.Format("2006-01-02 15:04:05 -07:00"),
			change.Stop.Format("2006-01-02 15:04:05 -07:00"),
			change.Duration,
			change.Description,
			change.NewDescription,
			change.ProjectName,
			change.ProjectID,
			change.NewProjectName,
			change.NewProjectID,
		)
	}
	writer.Flush()
}

func latestEntries(entries []rewrite.Entry, count int) []rewrite.Entry {
	if len(entries) <= count {
		return entries
	}
	return entries[:count]
}

func latestChanges(changes []rewrite.Change, count int) []rewrite.Change {
	if len(changes) <= count {
		return changes
	}
	return changes[:count]
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: toggl-automations <command> [options]")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Commands:")
	fmt.Fprintln(output, "  auth login       Save the Toggl API token")
	fmt.Fprintln(output, "  auth status      Show whether a token is configured")
	fmt.Fprintln(output, "  auth logout      Remove the stored token")
	fmt.Fprintln(output, "  entries rewrite  Find, preview, or update time entries")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Options:")
	fmt.Fprintln(output, "  -h, --help        Show help")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Run 'toggl-automations <command> --help' for command-specific help.")
}

func printAuthUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: toggl-automations auth <command>")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Commands:")
	fmt.Fprintln(output, "  login [--from-env]  Save the Toggl API token")
	fmt.Fprintln(output, "  status              Show the active token source and masked suffix")
	fmt.Fprintln(output, "  logout              Remove tokens from persistent storage")
}

func printRewriteUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage:")
	fmt.Fprintln(output, "  toggl-automations entries rewrite --description X --project Y [options]")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Without replacement flags the command searches only. Replacement flags preview")
	fmt.Fprintln(output, "by default; add --apply to perform the bulk update.")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Options:")
	fmt.Fprintln(output, "  --description string      Exact source description (required)")
	fmt.Fprintln(output, "  --project string          Exact source project name or id:<number> (required)")
	fmt.Fprintln(output, "  --new-description string  Replacement description")
	fmt.Fprintln(output, "  --new-project string      Replacement project name or id:<number>")
	fmt.Fprintln(output, "  --apply                   Apply changes instead of previewing them")
	fmt.Fprintln(output, "  -h, --help                Show help")
}
