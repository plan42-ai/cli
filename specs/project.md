# Plan42 CLI - Project Specification

## Overview

**Project Name:** Plan42 CLI - Remote Runner Service
**Organization:** Plan42-AI
**Language:** Go (1.25.5)
**Repository:** github.com/plan42-ai/cli

### Purpose

A distributed task execution platform that enables remote execution of AI agent tasks. The runner service:
- Polls a Plan42 API server for job execution requests
- Executes jobs via Docker containers on local/remote systems
- Manages queue-based task distribution with auto-scaling
- Integrates with GitHub Enterprise for repository and PR feedback collection
- Provides configuration management for runner instances

### Key Functionalities

1. **Remote Job Execution** - Polls queues for incoming tasks and executes them in isolated containers
2. **GitHub Integration** - Fetches PR feedback, lists repos/branches, manages GitHub connections
3. **Runner Management** - Configuration, startup/shutdown, monitoring, and logging
4. **Queue Management** - Dynamic queue creation/deletion, load balancing
5. **macOS Service Integration** - LaunchAgent support for background execution

---

## Architecture

### Three-Executable Architecture

```
plan42
├── Runner Service (plan42-runner)
├── Runner Configuration (plan42-runner-config)
└── Runner Management CLI (plan42 runner)
```

### Component Layers

1. **CLI Layer** (`cmd/plan42/`, `cmd/plan42-runner/`, `cmd/plan42-runner-config/`)
   - User-facing entry points
   - Command parsing using Kong framework
   - Delegates heavy lifting to internal packages

2. **Core Processing Layer** (`internal/`)
   - **Poller** - Central message processing engine
   - **Runners** - Task execution coordination
   - **GitHub** - API integration
   - **Config** - Configuration management

3. **Platform Layer** (`internal/apple/`, `internal/launchctl/`)
   - macOS-specific service management
   - LaunchAgent plist generation and control

4. **Utilities** (`internal/util/`, `internal/docker/`)
   - Helper functions and Docker parsing

---

## Key Source Files

### Entry Points

| File | Role | Purpose |
|------|------|---------|
| `cmd/plan42/main.go` | CLI Entry | Runner management commands (enable, disable, logs, status, etc.) |
| `cmd/plan42-runner/main.go` | Runner Service | Main service process - polls and executes tasks |
| `cmd/plan42-runner-config/main.go` | Config TUI | Interactive configuration tool using Bubble Tea |

### Core Packages

| Package | Role |
|---------|------|
| `internal/poller/` | Message polling, task dispatching, queue management, auto-scaling |
| `internal/github/` | GitHub API client (REST + GraphQL) for PR feedback, repo listing |
| `internal/cli/runner/` | Runner service options and initialization |
| `internal/cli/runnerconfig/` | Config processing |
| `internal/config/` | Configuration struct definitions |
| `internal/apple/container/` | macOS container/job management |
| `internal/launchctl/` | LaunchAgent plist generation and control |
| `internal/docker/` | Docker image/container parsing |
| `internal/util/` | Utility functions (pointers, exit codes, paths) |

---

## Commands and Features

### Plan42 CLI Commands (`plan42` binary)

```
plan42 runner
├── config          - Open interactive config editor
├── enable          - Register as launchd service (macOS only)
├── exec            - Execute the runner service (delegates to plan42-runner)
├── stop            - Stop the runner service
├── status          - Show service status
├── logs [-f]       - View service logs (with follow option)
├── disable         - Unregister from launchd
└── job
    ├── list [-a]    - List runner jobs (all with -a flag)
    ├── kill <id>    - Terminate a running job
    ├── logs <id>    - View job-specific logs
    └── prune        - Delete completed job logs
```

### Plan42-Runner Service (`plan42-runner` binary)

- Executes as background service
- Reads config from `~/.config/plan42-runner.toml`
- Continuously polls Plan42 API for task messages
- Dynamically manages execution queues
- Auto-scales based on load (scales up at 80%+, down at 40%-)
- Gracefully drains queues on shutdown (5-minute timeout)

### Plan42-Runner-Config Tool (`plan42-runner-config` binary)

- Interactive configuration interface using Bubbletea framework
- Configures runner token, server URL, GitHub connections

---

## Dependencies

### Key Dependencies

| Dependency | Purpose |
|-----------|---------|
| alecthomas/kong | CLI argument parsing |
| charmbracelet/bubbletea | Terminal UI framework |
| charmbracelet/bubbles | UI components (spinner, text input) |
| charmbracelet/lipgloss | Terminal styling |
| google/go-github | GitHub API client |
| pelletier/go-toml | TOML config parsing |
| plan42-ai/sdk-go | Plan42 API SDK |
| plan42-ai/ecies | Encryption (ECDSA/ECIES) |
| plan42-ai/log | Structured logging |

### Technologies

- **Go 1.25.5** - Modern Go with latest features
- **ECIES Encryption** - End-to-end message encryption (EC P-256)
- **GraphQL** - GitHub API querying for PR feedback
- **Docker** - Container image/command parsing
- **LaunchAgent** - macOS background service management
- **TOML** - Configuration file format
- **JWT** - Token parsing for runner authentication

---

## Configuration

### Configuration File

Location: `~/.config/plan42-runner.toml`

```toml
[runner]
url = "https://api.dev.plan42.ai"
token = "p42r_<jwt_token>"
skip_ssl_verify = false

[github.<name>]
name = "my-github"
url = "https://github.com"
connection_id = "<uuid>"
token = "<github_token>"
```

### Build System

**Makefile Targets:**
- `make build` - Builds all three binaries
- `make package` - Builds and packages into distribution artifacts
- `make test` - Runs all tests
- `make lint` - Runs golangci-lint
- `make fmt` - Formats code
- `make clean` - Cleans artifacts

**Versioning Format:** `<MAJOR>.<MINOR>.<PATCH>[-<ATTEMPT>]`

---

## Data Flow

### Execution Flow

```
Runner Service Start
├─ Read TOML config
├─ Initialize Plan42 SDK Client
├─ Create GitHub client connections
├─ Start Poller with initial queue
└─ Start Scale monitoring goroutine

Polling Loop (per queue)
├─ Poll API for message batch (30s timeout)
├─ For each message:
│  ├─ Decrypt ECIES payload
│  ├─ Parse message type
│  ├─ Route to handler (Ping, Invoke, GitHub)
│  ├─ Process and get response
│  ├─ Encrypt response
│  └─ Write response back to API
└─ Track batch utilization for scaling

Scale Loop (1s intervals)
├─ Check average batch utilization
├─ If >= 80% for 1+ min: Scale up (double queues)
├─ If <= 40% for 2+ min: Scale down (reduce by 1)
└─ Reset statistics

Graceful Shutdown
├─ Signal all queues to drain
├─ Wait for in-flight messages (30s per queue)
├─ Mark queues as draining
├─ Delete queues from API
└─ Exit
```

### Message Types

1. **PingRequest** - Health check (no-op response)
2. **InvokeAgentRequest** - Docker container execution with task details
3. **ListOrgsForGithubConnectionRequest** - GitHub org listing (paginated)
4. **SearchRepoRequest** - Repository search within org (paginated)
5. **ListRepoBranchesRequest** - Branch listing for repo (paginated)

---

## Architecture Patterns

### Message-Driven Architecture
- Poller receives encrypted messages from API
- Messages parsed into type-specific handlers
- Each handler processes and returns response
- Responses encrypted and sent back

### Queue-Based Concurrency
- Multiple execution queues (starts with 1)
- Auto-scales based on batch utilization
- Each queue runs in separate goroutine
- Scale up: doubles queue count at 80%+ full
- Scale down: reduces by 1 at 40%- full
- Graceful drain on shutdown (30-second per queue)

### Encryption
- ECIES (Elliptic Curve Integrated Encryption Scheme)
- Each queue generates P-256 keypair
- Messages encrypted with caller's public key
- Ensures end-to-end security

### Platform Abstraction
- Darwin (macOS) specific code in `*_darwin.go` files
- Generic implementations in `*_other.go` files
- LaunchAgent only on macOS

---

## Security

- **ECIES Encryption** - All message payloads encrypted end-to-end
- **Token Validation** - JWT token parsing and validation
- **Container Isolation** - Tasks run in isolated Docker containers
- **OAuth2** - GitHub integration uses OAuth2 tokens
- **File Permissions** - Config file created with 0600 permissions
- **Secure Random** - Uses `crypto/rand` for key generation

---

## CI/CD

### GitHub Actions Workflows

1. **PR Workflow** (`pr.yml`)
   - Triggers on PRs to main/hack branches
   - Runs on: Ubuntu (x86/ARM64), macOS (Intel/Apple Silicon)
   - Steps: Checkout → Go setup → Install deps → golangci-lint → Test → Build/Package

2. **Merge Workflow** (`merge.yml`)
   - Triggers on merges to main
   - Same platforms and testing as PR
   - Creates GitHub release with built artifacts

### Testing Strategy

- Unit tests in `*_test.go` files
- CI testing on all platforms (Linux x86/ARM64, macOS Intel/Apple Silicon)
- Tests must pass on all platforms before merge
