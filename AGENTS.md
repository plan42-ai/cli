# CLI Repository Agent Instructions

## Before Completing Work

**Always ensure the following pass before finishing a task or creating a PR:**

```bash
make build
make lint
make test
```

Fix any issues before committing.

## Common Lint Issues

- Stuttering type names (e.g., `runtime.RuntimeProvider` should be `runtime.Provider`)
- Unused variables or imports
- Missing error handling

## PR Requirements

1. Code must compile (`make build`)
2. Linter must pass (`make lint`)
3. Tests must pass (`make test`)
