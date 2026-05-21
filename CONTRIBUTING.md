# Contributing

Thanks for your interest in `whoop-mcp`.

## Quick start

```sh
go mod download
go test ./...
go vet ./...
gofmt -l .
golangci-lint run        # optional but matches CI
docker build -t whoop-mcp .
```

## Branch and commit style

- Branch names use `{change_type}/{short-name}`, e.g. `feat/add-strain-tool`,
  `fix/oauth-state-cookie`, `docs/readme-deploy`.
- Keep commits focused and self-describing. The PR body is the right place
  for context; the commit message should explain the *why* in one or two
  sentences when the *what* isn't self-evident from the diff.
- No emojis in commits, PR titles, or branch names.

## Pull requests

- PR titles are short and imperative. The description carries detail.
- Link the issue you're closing if there is one.
- CI must be green before review.

## Code expectations

- Standard Go formatting (`gofmt`). CI fails on unformatted files.
- New behavior comes with tests — see `internal/store/store_test.go` and
  `internal/auth/auth_test.go` for the style.
- Don't add dependencies for things the standard library already does well.
- Don't introduce abstractions that aren't used by at least two callers.
- Public functions and types should carry doc comments explaining their
  contract — not their implementation.

## Security

Don't open public issues for security problems. See [SECURITY.md](./SECURITY.md).
