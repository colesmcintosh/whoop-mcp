# Contributing

Thanks for your interest in `whoop-mcp`.

## Quick start

```sh
bun install
bun test
bun run typecheck
bun run lint
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

- Standard TypeScript strict mode; `bun run typecheck` and `bun run lint`
  must pass. CI runs both.
- New behavior comes with tests — see `tests/` for the style (bun:test,
  fixture HTTP servers for anything that talks to Whoop or the MCP client).
- Don't add dependencies for things `fetch`/`node:*` already do well.
- Don't introduce abstractions that aren't used by at least two callers.
- Exported functions and types should carry doc comments explaining their
  contract — not their implementation.

## Security

Don't open public issues for security problems. See [SECURITY.md](./SECURITY.md).
