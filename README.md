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

The token is read only from `TOGGL_API_TOKEN`. Do not pass it as a command-line
argument or commit it to a file in this repository.

For a shell session without placing the token in shell history:

```sh
read -rsp "Toggl API token: " TOGGL_API_TOKEN
echo
export TOGGL_API_TOKEN
```

## Usage

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
