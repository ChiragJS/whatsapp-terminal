# Contributing

## Workflow

1. Create a branch from `main`.
2. Make a focused change with matching tests and docs.
3. Run:

```bash
go test ./...
go build ./...
go vet ./...
```

4. Open a pull request with:

- a short problem statement
- the implementation summary
- verification commands and results
- screenshots or terminal recordings for UI changes

## Commit Style

Use Conventional Commits:

- `feat: add QR status view`
- `fix: avoid duplicate unread counts`
- `docs: clarify session storage`

## Scope Rules

- Keep `whatsmeow` usage inside `internal/transport/whatsapp/`.
- Preserve backward-compatible CLI flags unless the PR clearly documents a break.
- Prefer small, reviewable patches over broad refactors.
