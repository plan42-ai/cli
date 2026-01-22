# Project notes

## Proxy
- Outbound traffic from our VPCs and runners should go through the debugging-sucks proxy-service, which is a Go-based TLS-inspecting forward proxy behind the AWS GWLB endpoints.
- The proxy enforces an allow list defined in the proxy-service repository; new destinations need to be added there and rolled out via proxy-service-infra before they are reachable.
- For local work, set HTTPS_PROXY/HTTP_PROXY to the environment-specific proxy endpoint and trust the internal CA bundle when TLS interception is enabled.

## Tech stack
- Go 1.25.5 module-based service/library

## Conventions
- Use gofmt (and goimports where available) before committing changes.
- Run go test ./... locally; prefer Makefile targets for builds and packaging.
- Manage dependencies with go mod tidy when module imports change.

## AI tools
- codex
- claude
