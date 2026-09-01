# Toggl Automations

[![CI](https://github.com/ihoru/toggl-automations/actions/workflows/ci.yml/badge.svg)](https://github.com/ihoru/toggl-automations/actions/workflows/ci.yml)

A small, safety-first Go CLI for previewing and bulk-rewriting your own
[Toggl Track](https://toggl.com/track/) time entries.

The first automation finds entries with an exact description and project,
shows a preview, and can replace the description, project, or both. It searches
from the account creation date, ignores other users' entries, and skips the
currently running timer.

## Requirements

- Go 1.24 or newer
- A Toggl Track API token from your profile

## Install

```sh
go install github.com/ihoru/toggl-automations/cmd/toggl-automations@latest
```

For local development:

```sh
git clone https://github.com/ihoru/toggl-automations.git
cd toggl-automations
go build -o bin/toggl-automations ./cmd/toggl-automations
```

## Configure the API token

Save the token once using a hidden terminal prompt:

```sh
toggl-automations auth login
```

The command stores the token in the operating system keyring. If no keyring is
available, it falls back to `$XDG_CONFIG_HOME/toggl-automations/token` (or
`~/.config/toggl-automations/token` by default) and requires file permissions
`0600`.

Check or remove the stored credential without printing the full token:

```sh
toggl-automations auth status
toggl-automations auth logout
```

`TOGGL_API_TOKEN` remains the highest-priority override for CI and temporary
shell sessions. To import an already exported value into persistent storage,
run `toggl-automations auth login --from-env` once. Do not pass tokens as
command-line arguments or commit them to the repository.

## Usage

List entries started during the last 48 hours, oldest first:

```sh
toggl-automations entries list
```

Each output line contains `start | finish | project | duration HH:MM |
description` in the timezone configured in Toggl. A running entry uses
`RUNNING` as its finish value and shows its elapsed duration.

Project selectors accept either an exact, case-sensitive name or an explicit
ID in the form `id:123456`. A project name must be unique across your accessible
workspaces; otherwise, use its ID.

Search only. This prints the total number of matches and the latest ten:

```sh
toggl-automations entries rewrite \
  --description "X" \
  --project "Y"
```

Preview a description and project replacement:

```sh
toggl-automations entries rewrite \
  --description "X" \
  --project "Y" \
  --new-description "Z" \
  --new-project "J"
```

Apply the same replacement after reviewing the preview:

```sh
toggl-automations entries rewrite \
  --description "X" \
  --project "Y" \
  --new-description "Z" \
  --new-project "J" \
  --apply
```

`--new-description` and `--new-project` are independent. Omit either one to
leave that field unchanged. Empty descriptions and removing a project are not
supported.

## Safety model

- Search and preview never call a mutation endpoint.
- Matching is rechecked locally because the Reports API description filter is
  broader than this CLI's exact-match contract.
- Only the authenticated user's entries are considered.
- The currently running timer is skipped.
- Bulk updates contain at most 100 IDs and use idempotent JSON Patch `replace`
  operations.
- Toggl bulk updates are not transactional. The CLI reports every per-entry
  failure and exits non-zero if a batch is only partially successful.
- Authentication data is never included in diagnostics.

The Reports API is queried in bounded date windows and paginated with
`X-Next-Row-Number`, so an account history larger than one response page is not
silently truncated.

## Development

```sh
gofmt -w .
go vet ./...
go test -race ./...
go build ./cmd/toggl-automations
```

Tests use local HTTP servers and do not require a real Toggl token.

## License

[MIT](LICENSE)
