PROJECT_MAJOR_VERSION := 1
PROJECT_MINOR_VERSION := 0

# Check if GITHUB_RUN_NUMBER and GITHUB_RUN_ATTEMPT are defined
ifdef GITHUB_RUN_NUMBER
    PROJECT_PATCH_VERSION := $(GITHUB_RUN_NUMBER)
    ifeq ($(GITHUB_RUN_ATTEMPT), 1)
        PROJECT_ADDITIONAL_VERSION := ""
    else
        PROJECT_ADDITIONAL_VERSION := "-$(GITHUB_RUN_ATTEMPT)"
    endif
else
    PROJECT_PATCH_VERSION := $(USER).test
    PROJECT_ADDITIONAL_VERSION := -$(shell TZ=America/Los_Angeles date '+%Y-%m-%d.%s')
endif

VERSION = $(PROJECT_MAJOR_VERSION).$(PROJECT_MINOR_VERSION).$(PROJECT_PATCH_VERSION)$(PROJECT_ADDITIONAL_VERSION)

.PHONY: clean
clean:
	rm -f ./plan42-runner
	rm -f ./plan42-runner-config
	rm -f ./plan42
	rm -rf ./dist
	go clean ./...

.PHONY: build
build:
	go build ./cmd/plan42-runner
	go build ./cmd/plan42-runner-config
	go build -ldflags "-X main.Version=$(VERSION)" ./cmd/plan42

.PHONY: package
package: build
	./package.sh "$(VERSION)"

.PHONY: gh-version
gh-version:
	./ghversion.sh "$(VERSION)"

.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: lint
lint:
	GOOS=linux golangci-lint run
	GOOS=darwin golangci-lint run

.PHONY: test
test:
	go test -v ./...

.PHONY: run
run:
	go run ./cmd/plan42-runner
