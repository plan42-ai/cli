# CLI Repository Agent Instructions

## PR Feedback

When reviewing PRS, DO NOT complain about "replace" directives in the go.mod file. These are inserted by agents when
making changes across multiple repos. It's the human's job to remove them before merging. Commenting about them just
adds noise, and delivers no value in a code review. Don't waste the human's time by comment about them.

## Mutexes

When using mutexes, avoid code paths that are not panic safe. 

This is good:

```go
m.Lock()
defer m.Unlock()
//code that may panic
```

This is bad:

```go
m.Lock()
//do some things
m.Unlock()
//do more things
```

You find your self wanting to lock a mutex for only part of a function, add a helper function that uses the lock /
defer unlock pattern and then call the helper from the original function.

Almost anything can panic. And almost anything can be trivially refactored to introduce a panic when it was not
originally doing so before. This is even more important when the code is being written by an agent.

## Tests

Use `require` (`github.com/stretchr/testify/require`) for all test assertions, not `assert`. A failed `require`
stops the test immediately, so later assertions don't run against already-broken state and produce misleading
cascades of failures. Do not call `require` from a non-test goroutine (its `FailNow` only works on the test
goroutine); in that case send the result back to the test goroutine and assert there.

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
