# Project Context

## Purpose
Provide the Plan42 runner command-line tools used to interact with the service, configure authentication, and run remote jobs from developer machines or CI.

## Tech Stack
- Go 1.25
- CLI parsing with kong and a Bubble Tea TUI for interactive flows
- plan42 shared libraries (sdk-go, openid, concurrency, log, xml)
- GitHub client libraries for release automation

## Project Conventions

### Code Style
Standard Go formatting via `go fmt`. `make lint` runs golangci-lint for both linux and macOS builds. Keep command and flag names consistent across binaries.

### Architecture Patterns
Multiple binaries under `cmd/` (`plan42-runner`, `plan42-runner-config`, `plan42`) share internal packages for API access and auth. Terminal UI components are built with Bubble Tea and lipgloss, while kong handles CLI argument parsing.

### Testing Strategy
`go test ./...` covers package logic. Run linting and tests before packaging or tagging releases.

### Git Workflow
Feature branches with PR review into main. Use the `gh-version` and `package` targets to create versioned artifacts when preparing releases.

## Domain Context
The runner authenticates against the Plan42 platform using OpenID flows and the shared SDK. Commands may manage remote execution, configuration files, and clipboard helpers for tokens.

## Important Constraints
- Binaries target linux and macOS; ensure changes keep cross-platform terminal behavior intact.
- Auth flows rely on browser or device-code style interactions; avoid breaking prompts and clipboard support.

## External Dependencies
- Plan42 APIs via `sdk-go`
- OpenID provider used for authentication
- GitHub releases for distributing tagged binaries
