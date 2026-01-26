# Plan42 CLI - Project Specification

## Overview

The Plan42 CLI is a remote runner system that enables Plan42 to execute AI agent tasks on user machines. It acts as a bridge between the Plan42 cloud service and local compute resources, allowing AI agents to run in isolated containers with access to private GitHub repositories.

## Purpose

The CLI solves the problem of executing AI agent workloads on private infrastructure:
- Runs AI agent containers locally on user machines
- Provides secure access to private GitHub repositories
- Enables Plan42 to offload compute to user-controlled environments
- Maintains security through ECIES encryption for all message payloads

## Architecture

### Executables

The project builds three binaries:

1. **`plan42`** - Main CLI entry point
   - Provides subcommands for runner management
   - Delegates actual work to sibling executables via `syscall.Exec`
   - Commands: `runner enable|disable|stop|status|logs|config|exec|job`

2. **`plan42-runner`** - The polling service
   - Long-running daemon that polls Plan42 for work
   - Manages message queues with auto-scaling
   - Executes agent containers
   - Entry point: `cmd/plan42-runner/main.go`

3. **`plan42-runner-config`** - Configuration TUI
   - Interactive terminal UI for configuring the runner
   - Built with Bubble Tea (Charm libraries)
   - Validates tokens and configures GitHub connections
   - Entry point: `cmd/plan42-runner-config/main.go`

### Directory Structure

```
cli/
├── cmd/
│   ├── plan42/                  # Main CLI
│   ├── plan42-runner/           # Polling service
│   └── plan42-runner-config/    # Config TUI
├── internal/
│   ├── apple/container/         # macOS container integration
│   ├── cli/runner/              # Runner options processing
│   ├── cli/runnerconfig/        # Config options processing
│   ├── config/                  # TOML config structures
│   ├── docker/                  # Docker image URI parsing
│   ├── github/                  # GitHub API client
│   ├── launchctl/               # macOS launchd integration
│   ├── poller/                  # Message polling & processing
│   └── util/                    # Common utilities
└── specs/                       # Project documentation
```

## Key Components

### Poller (`internal/poller/`)

The core message processing system:

- **Queue Management**: Creates ephemeral queues with ECDSA key pairs for ECIES encryption
- **Auto-scaling**: Dynamically scales queue count based on batch utilization
  - Scale up: When average batch >= 80% full for 1+ minute, doubles queues
  - Scale down: When average batch <= 40% full for 2+ minutes, removes one queue
- **Message Types**:
  - `PingRequest` - Health checks
  - `InvokeAgentRequest` - Execute an AI agent in a container
  - `ListOrgsForGithubConnectionRequest` - List GitHub organizations
  - `SearchRepoRequest` - Search GitHub repositories
  - `ListRepoBranchesRequest` - List repository branches

Key file: `internal/poller/poller.go:211-261` (poll loop)

### Container Execution (`internal/poller/invoke_darwin.go`)

Agent invocation on macOS:
- Validates task ID (must be UUID) and Docker image URL
- Pulls container image using `container` CLI tool
- Runs container with resource limits (4 CPUs, 8GB RAM)
- Passes encrypted request JSON via stdin
- Logs output to `~/Library/Logs/ai.plan42.runner/<job-id>`

Key file: `internal/poller/invoke_darwin.go:37-73` (Process method)

### GitHub Integration (`internal/github/`)

GitHub API client supporting:
- REST API via `go-github` library
- GraphQL API for PR feedback retrieval
- GitHub Enterprise URL configuration
- PR review threads, issue comments, and review comments

Key file: `internal/github/client.go:85-105` (GetPRFeedBack)

### launchctl Integration (`internal/launchctl/`)

macOS service management:
- Generates launchd plist configurations
- Manages service lifecycle (bootstrap, kickstart, shutdown)
- Configures KeepAlive and RunAtLoad settings
- Handles logging configuration

Key file: `internal/launchctl/launchctl.go:136-153` (Create method)

### Configuration (`internal/config/`)

TOML-based configuration:
```toml
[runner]
url = "https://api.plan42.ai"
token = "p42r_..."
skip_ssl_verify = false  # optional

[github.connection-name]
name = "..."
url = "https://github.com"  # or enterprise URL
connection_id = "..."
token = "ghp_..."
```

## Data Flow

1. **Initialization**: Runner parses JWT from `p42r_` token to extract tenant ID and runner ID
2. **Queue Registration**: Creates queue with ECDSA public key, registers with Plan42 server
3. **Polling**: Long-polls `GetMessagesBatch` endpoint (30s timeout)
4. **Message Processing**:
   - Decrypts ECIES payload using queue's private key
   - Parses message type and dispatches to handler
   - Encrypts response using caller's public key
   - Writes response via `WriteResponse` endpoint
5. **Agent Execution** (for InvokeAgentRequest):
   - Fetches PR feedback if needed
   - Pulls Docker image
   - Spawns container with request JSON on stdin
   - Streams output to log file

## Dependencies

### Plan42 Internal Libraries
- `github.com/plan42-ai/sdk-go` - API client
- `github.com/plan42-ai/ecies` - ECIES encryption
- `github.com/plan42-ai/concurrency` - Context groups and backoff
- `github.com/plan42-ai/openid` - JWT parsing
- `github.com/plan42-ai/log` - Structured logging
- `github.com/plan42-ai/xml` - XML marshaling for plists

### External Libraries
- `github.com/alecthomas/kong` - CLI argument parsing
- `github.com/charmbracelet/bubbletea` - TUI framework
- `github.com/charmbracelet/bubbles` - TUI components
- `github.com/charmbracelet/lipgloss` - TUI styling
- `github.com/google/go-github/v81` - GitHub REST API
- `github.com/pelletier/go-toml/v2` - TOML parsing
- `golang.org/x/oauth2` - OAuth2 HTTP client

## Platform Support

- **Primary**: macOS (darwin) - Full support with launchd integration
- **Secondary**: Linux - Container execution only (no service management)

The `container` CLI tool (Apple's container runtime) is required for agent execution on macOS.

## Security

- All message payloads encrypted with ECIES (Elliptic Curve Integrated Encryption Scheme)
- Per-queue ECDSA P-256 key pairs
- Runner tokens are JWTs with embedded runner ID and tenant ID
- Docker image URLs validated before execution
- Task IDs validated as UUIDs to prevent injection
