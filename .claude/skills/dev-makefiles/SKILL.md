---
name: dev-makefiles
description: Use when creating a Makefile, modifying a Makefile, adding make targets, setting up a build system, creating makehelp.sh, or migrating an existing Makefile to the 2-layer convention.
argument-hint: [action] [details]
---

# Building Makefiles

Create and maintain Makefiles using the 2-layer system. If `$ARGUMENTS` is provided, interpret it as the task (e.g., "add test-e2e target", "create new Makefile for Python project", "migrate existing Makefile").

## Task Workflow

Follow these steps in order. The first decision determines which path to take.

### Step 1: Assess the situation

1. Check if a `Makefile` already exists in the project root
2. Check if a `makehelp.sh` already exists
3. Identify the project's language/toolchain (inspect go.mod, package.json, Cargo.toml, pyproject.toml, etc.)

### Step 2: Choose the right path

**Path A — New Makefile (no Makefile exists):**
1. Identify the project's toolchain from existing config files
2. Set the Variables section using the Toolchain Adaptation table below
3. Generate both `Makefile` and `makehelp.sh` from the templates
4. Replace all `myapp` placeholders with the actual binary/project name
5. Make makehelp.sh executable: `chmod +x makehelp.sh`

**Path B — Add/modify targets (Makefile exists and follows 2-layer convention):**
1. Read the existing Makefile and makehelp.sh
2. Identify the correct `##@` section for the new target
3. Add the target with `.PHONY`, `## help text`, and recipe
4. If the recipe needs >10 lines or OS branching, delegate to makehelp.sh
5. If delegating: add both the `cmd_function()` and dispatcher entry in makehelp.sh

**Path C — Migrate existing Makefile (exists but doesn't follow 2-layer convention):**
1. Read the existing Makefile thoroughly — preserve ALL existing targets and behavior
2. Reorganize targets into the seven `##@` sections (General, Build, Test, Quality, Install, Development, Cleanup)
3. Add `.PHONY` declarations and `## help text` to every target
4. Add the `help` target and awk recipe
5. Extract any inline shell logic >10 lines into a new makehelp.sh
6. Create makehelp.sh with dispatcher if any logic was extracted
7. Verify: every original target still works the same way

### Step 3: Validate

- Run `make help` to verify help output renders correctly
- Confirm all `##@` sections appear with their targets
- If makehelp.sh was created/modified, verify it's executable and the dispatcher covers all delegated targets

## The 2-Layer System

```
+-------------------------------------+
|           Makefile                   |  <- Declarative interface (WHAT)
|  targets, dependencies, help text   |     Short commands, readable
+-----------+-------------------------+
            | delegates via @./makehelp.sh <cmd>
+-----------v-------------------------+
|          makehelp.sh                 |  <- Imperative logic (HOW)
|  OS branching, multi-step ops,      |     Testable, maintainable
|  anything >10 lines                 |
+-------------------------------------+
```

## The Mantra (3 Rules)

1. **"If it's a build-time operation, it's in make"** — compiling, testing, linting, formatting, installing dev tools
2. **"If it's a runtime operation, it's in the CLI"** — daemon management, service lifecycle, version queries
3. **"Complex shell logic belongs in makehelp.sh, not inline"** — anything >10 lines, OS branching, multi-step ops

## Toolchain Adaptation

When creating a Makefile, set the Variables section based on the project's language. Use this table:

| Variable | Go | Node/TypeScript | Python | Rust | Generic |
|----------|----|----|--------|------|---------|
| `BUILD_CMD` | `go build` | `npm run build` | `python -m build` | `cargo build` | `echo "no build step"` |
| `TEST_CMD` | `go test ./...` | `npm test` | `pytest` | `cargo test` | `echo "no tests"` |
| `LINT_CMD` | `golangci-lint run` | `eslint .` | `ruff check .` | `cargo clippy` | `echo "no linter"` |
| `FMT_CMD` | `gofmt -w .` | `prettier --write .` | `ruff format .` | `cargo fmt` | `echo "no formatter"` |
| `BINARY_DIR` | `./bin` | `./dist` | `./dist` | `./target` | `./build` |
| `COVERAGE_FILE` | `coverage.out` | `coverage/lcov.info` | `htmlcov/` | `tarpaulin-report.html` | `coverage/` |
| `CLEAN_EXTRAS` | `vendor` | `node_modules` | `__pycache__ *.egg-info .venv` | `target` | _(none)_ |

For the `build` target recipe, adapt accordingly:
- **Go**: `@$(BUILD_CMD) -o $(BINARY_DIR)/myapp ./cmd/myapp`
- **Node**: `@$(BUILD_CMD)`
- **Python**: `@$(BUILD_CMD)` (or skip if no build step)
- **Rust**: `@$(BUILD_CMD) --release` (for production)

Omit sections that don't apply (e.g., no `build-production` for a pure Python library), but keep the `##@` section header with at least one target in each section.

## Complete Makefile Template

**Include ALL seven ##@ sections. Adapt commands to the project's toolchain.**

```makefile
# ============================================================================
# Project Makefile
# ============================================================================
# 2-Layer System: This Makefile defines WHAT to do.
# Complex logic (>10 lines, OS branching) goes in makehelp.sh (HOW to do it).
# ============================================================================

.DEFAULT_GOAL := help

# ---------------------------------------------------------------------------
# Variables — adapt to your language/toolchain (see Toolchain Adaptation table)
# ---------------------------------------------------------------------------

# Tools
BUILD_CMD := go build
TEST_CMD := go test
LINT_CMD := golangci-lint run
FMT_CMD := gofmt -w .

# Paths
BINARY_DIR := ./bin
COVERAGE_FILE := coverage.out

# Derived
VERSION := $(shell git describe --tags 2>/dev/null || echo "0.0.0-dev")

# ---------------------------------------------------------------------------
# Targets
# ---------------------------------------------------------------------------

##@ General

.PHONY: help
help:  ## Display this help message
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} \
		/^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } \
		/^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)

.PHONY: prereqs
prereqs:  ## Check and install prerequisites
	@./makehelp.sh prereqs

##@ Build

.PHONY: build
build:  ## Build for local platform (debug mode)
	@$(BUILD_CMD) -o $(BINARY_DIR)/myapp ./cmd/myapp

.PHONY: build-production
build-production:  ## Build optimized production binaries
	@./makehelp.sh build-production $(VERSION)

##@ Test

.PHONY: test
test:  ## Run all tests
	@$(TEST_CMD) ./...

.PHONY: test-unit
test-unit:  ## Run unit tests only (fast, no external deps)
	@$(TEST_CMD) -short ./...

.PHONY: test-integration
test-integration:  ## Run integration tests (requires infrastructure)
	@$(TEST_CMD) -run Integration ./...

##@ Quality

.PHONY: lint
lint:  ## Run linter
	@$(LINT_CMD) ./...

.PHONY: fmt
fmt:  ## Format code
	@$(FMT_CMD)

.PHONY: check
check: lint test  ## Run lint + test together

##@ Install

.PHONY: install
install: install-dev  ## Alias for install-dev

.PHONY: install-dev
install-dev: build  ## Build and install as symlinks (for development)
	@./makehelp.sh install-dev

.PHONY: install-production
install-production: build-production  ## Build and install as copies (for production)
	@./makehelp.sh install-production

.PHONY: uninstall
uninstall:  ## Remove all installed files
	@./makehelp.sh uninstall

##@ Development

.PHONY: dev
dev: fmt test build  ## Format, test, and build

.PHONY: cycle
cycle: uninstall clean build install  ## Full clean rebuild and install

.PHONY: run
run: build  ## Build and run development version
	@$(BINARY_DIR)/myapp

##@ Cleanup

.PHONY: clean
clean:  ## Remove build artifacts
	@rm -rf $(BINARY_DIR) $(COVERAGE_FILE)

.PHONY: clean-all
clean-all: clean  ## Deep clean including vendor and cache
	@rm -rf vendor
	@$(BUILD_CMD) clean -cache 2>/dev/null || true
```

## Complete makehelp.sh Template

**Every target that delegates to makehelp.sh must have a matching function and dispatcher entry.**

```bash
#!/usr/bin/env bash
set -euo pipefail

# ============================================================================
# makehelp.sh — Complex logic extracted from Makefile
# ============================================================================
# The Makefile defines WHAT; this script implements HOW for anything too
# complex for inline make recipes (>10 lines, OS branching, multi-step ops).
# ============================================================================

BINARY_DIR="./bin"
INSTALL_DIR="${HOME}/.local/bin"

# ---------------------------------------------------------------------------
# Helper functions
# ---------------------------------------------------------------------------

check_command() {
    if ! command -v "$1" &>/dev/null; then
        echo "Error: $1 is required but not installed" >&2
        return 1
    fi
}

# ---------------------------------------------------------------------------
# Command functions
# ---------------------------------------------------------------------------

cmd_prereqs() {
    echo "Checking prerequisites..."
    case "$(uname -s)" in
        Darwin)
            echo "macOS detected"
            check_command brew || { echo "Install Homebrew first: https://brew.sh" >&2; exit 1; }
            # Adapt: replace with your project's toolchain packages
            brew install go golangci-lint
            ;;
        Linux)
            echo "Linux detected"
            if command -v apt-get &>/dev/null; then
                sudo apt-get update && sudo apt-get install -y golang
            elif command -v dnf &>/dev/null; then
                sudo dnf install -y golang
            fi
            ;;
        *)
            echo "Unsupported OS: $(uname -s)" >&2
            exit 1
            ;;
    esac
    echo "All prerequisites satisfied"
}

cmd_build_production() {
    local version="${1:-$(git describe --tags 2>/dev/null || echo "0.0.0-dev")}"
    local ldflags="-s -w -X main.Version=${version}"
    local platforms=(
        "darwin/amd64"
        "darwin/arm64"
        "linux/amd64"
        "linux/arm64"
    )

    for platform in "${platforms[@]}"; do
        IFS='/' read -r os arch <<< "$platform"
        output="${BINARY_DIR}/myapp-${os}-${arch}"
        echo "Building ${output}..."
        CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -ldflags="${ldflags}" -o "$output" ./cmd/myapp
    done
}

cmd_install_dev() {
    # install-dev creates SYMLINKS so rebuilds work without reinstalling
    mkdir -p "$INSTALL_DIR"
    local binary="${BINARY_DIR}/myapp"
    ln -sf "$(pwd)/${binary}" "${INSTALL_DIR}/myapp"
    echo "Installed: ${INSTALL_DIR}/myapp -> $(pwd)/${binary} (symlink)"
}

cmd_install_production() {
    # install-production COPIES binaries (self-contained, works after source removal)
    mkdir -p "$INSTALL_DIR"
    local os arch binary
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch="$(uname -m)"
    [[ "$arch" == "x86_64" ]] && arch="amd64"
    [[ "$arch" == "aarch64" ]] && arch="arm64"
    binary="${BINARY_DIR}/myapp-${os}-${arch}"

    cp -f "$binary" "${INSTALL_DIR}/myapp"
    chmod +x "${INSTALL_DIR}/myapp"
    echo "Installed: ${INSTALL_DIR}/myapp (copy)"
}

cmd_uninstall() {
    rm -f "${INSTALL_DIR}/myapp"
    echo "Uninstalled: ${INSTALL_DIR}/myapp"
}

# ---------------------------------------------------------------------------
# Dispatcher — one entry per makehelp.sh-delegated target
# ---------------------------------------------------------------------------

case "${1:-}" in
    prereqs)            cmd_prereqs ;;
    build-production)   cmd_build_production "${2:-}" ;;
    install-dev)        cmd_install_dev ;;
    install-production) cmd_install_production ;;
    uninstall)          cmd_uninstall ;;
    *)
        echo "Usage: $0 {prereqs|build-production|install-dev|install-production|uninstall}" >&2
        exit 1
        ;;
esac
```

## Conventions

### Naming
- **Lowercase with hyphens**: `test-unit`, `build-production`, `install-dev`
- **Not**: `testUnit`, `build_production`, `TEST`

### Help Annotations
```makefile
##@ Section Name                    # Section header in help output
.PHONY: target-name
target-name:  ## Short description  # Help text for this target
	@command
```

### When to Delegate to makehelp.sh
- More than ~10 lines of shell in a single target
- OS-specific branching (Darwin vs Linux)
- Multi-step operations with user prompts
- Reusable logic needed by multiple targets

### Key Target Relationships
- `install` is always an **alias** for `install-dev` (via dependency, no recipe)
- `install-dev` depends on `build` (builds first, then symlinks)
- `install-production` depends on `build-production` (builds optimized, then copies)
- `check` depends on `lint` and `test` (runs both)
- `dev` depends on `fmt`, `test`, `build` (developer workflow chain)
- `cycle` depends on `uninstall`, `clean`, `build`, `install` (full rebuild)
- `clean-all` depends on `clean` (extends it)

## Adding New Targets Checklist

1. Choose the right `##@` section (General, Build, Test, Quality, Install, Development, Cleanup)
2. Use `lowercase-with-hyphens` naming
3. Add `.PHONY` declaration
4. Add `## Description` help annotation
5. Decide: inline command or `@./makehelp.sh target-name`?
6. If makehelp.sh: add `cmd_target_name()` function AND dispatcher entry

## Edge Cases

**Empty ##@ section**: Keep the section header even if there's only a placeholder target. All seven sections must appear for consistency. Use a comment like `# No targets yet` only as last resort — prefer adding at least the standard target for that section.

**Project has no build step** (e.g., pure scripting language): Replace the `build` target recipe with `@echo "No build step required"` but keep the target so `dev` and `cycle` chains still work.

**Project has no installable binary**: Remove `install-dev`, `install-production`, and `uninstall` target *recipes* but keep stub targets with `@echo "Nothing to install"` so `cycle` doesn't break.

**Multiple binaries**: Add one `build-<name>` target per binary under `##@` Build, and have the main `build` target depend on all of them: `build: build-foo build-bar`.

**Makefile includes other Makefiles**: The help awk recipe uses `$(MAKEFILE_LIST)` so included targets will appear in help automatically. Ensure included files use the same `##@` and `##` conventions.

**Windows compatibility**: makehelp.sh is bash-only. If Windows support is needed, note it in the Makefile header and provide PowerShell alternatives in a separate `makehelp.ps1`, delegated the same way.

## What NOT to Do

- **Don't put runtime operations in make** — daemon start/stop, service management, and version queries belong in the CLI
- **Don't inline complex shell in Makefile recipes** — if it's >10 lines or has `if/case`, move it to makehelp.sh
- **Don't use tabs inconsistently** — Makefile recipes MUST use tabs, not spaces
- **Don't skip `.PHONY`** — every target that doesn't produce a file needs it
- **Don't omit `## help text`** — every target must appear in `make help` output

## See Also

- `REFERENCE.md` — Install modes explained, optional targets, Warden application example
