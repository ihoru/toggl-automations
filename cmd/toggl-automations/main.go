package main

import (
	"context"
	"os"

	"github.com/ihoru/toggl-automations/internal/cli"
)

func main() {
	os.Exit(cli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, os.Getenv, nil))
}
