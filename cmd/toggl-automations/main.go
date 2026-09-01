package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ihoru/toggl-automations/internal/cli"
	"github.com/ihoru/toggl-automations/internal/credentials"
	"golang.org/x/term"
)

func main() {
	store, err := credentials.NewDefault()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: initialize credential storage: %v\n", err)
		os.Exit(1)
	}
	os.Exit(cli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, cli.Dependencies{
		Getenv:      os.Getenv,
		Credentials: store,
		ReadSecret:  readSecret,
	}))
}

func readSecret(output io.Writer) (string, error) {
	fileDescriptor := int(os.Stdin.Fd())
	if !term.IsTerminal(fileDescriptor) {
		return "", errors.New("standard input is not a terminal; use auth login --from-env")
	}
	if _, err := fmt.Fprint(output, "Toggl API token: "); err != nil {
		return "", err
	}
	secret, err := term.ReadPassword(fileDescriptor)
	_, newlineErr := fmt.Fprintln(output)
	if err != nil {
		return "", err
	}
	if newlineErr != nil {
		return "", newlineErr
	}
	return string(secret), nil
}
