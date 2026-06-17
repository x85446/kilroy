# Makefile Reference

Detailed patterns, optional targets, and the Warden application of the 2-layer Makefile system.

## Install Modes Explained

### `install-dev` (Development)

Creates **symlinks** pointing to binaries in the build output directory:

```
~/.local/bin/myapp -> <project>/bin/myapp
```

- Rebuild without reinstall (`make build` updates the binary, symlink still works)
- Easy to identify: `ls -la ~/.local/bin/myapp` shows it's a symlink
- **When to use:** Daily development, testing local changes

### `install-production` (Production)

**Copies** actual binaries into place, overwriting any symlinks:

```
~/.local/bin/myapp    (actual file, not symlink)
```

- Self-contained installation, works after source directory is removed
- **When to use:** Testing production behavior, preparing releases, non-dev machines

### Quick Reference

| Scenario                  | Command                   |
|---------------------------|---------------------------|
| First-time dev setup      | `make install`            |
| After adding new binary   | `make install`            |
| After code changes        | `make build` (no reinstall needed) |
| Testing production        | `make install-production` |
| Return to dev mode        | `make install`            |

## Optional Targets by Category

Beyond the required targets in the template, these can be added as needed:

### Build (optional)

```makefile
.PHONY: build-local
build-local:  ## Cross-compile for local platform only
	@./makehelp.sh build-local

.PHONY: build-remote
build-remote:  ## Cross-compile for all non-local platforms
	@./makehelp.sh build-remote

.PHONY: build-all
build-all:  ## Cross-compile for all platforms (local + remote)
	@./makehelp.sh build-all
```

### Test (optional)

```makefile
.PHONY: test-race
test-race:  ## Unit tests with race detector
	@$(GO) test -race ./...

.PHONY: test-coverage
test-coverage:  ## Tests with coverage report
	@$(GO) test -coverprofile=$(COVERAGE_FILE) ./...

.PHONY: test-coverage-html
test-coverage-html: test-coverage  ## Generate HTML coverage report
	@$(GO) tool cover -html=$(COVERAGE_FILE)

.PHONY: test-pkg
test-pkg:  ## Run tests for specific package (PKG=internal/config)
	@$(GO) test ./$(PKG)/...

.PHONY: test-e2e
test-e2e:  ## End-to-end workflow tests
	@$(GO) test -tags=e2e ./...
```

### Development (optional)

```makefile
.PHONY: watch
watch:  ## Watch for changes and rebuild
	@./makehelp.sh watch
```

## makehelp.sh Patterns

### OS-Specific Branching

```bash
cmd_prereqs() {
    case "$(uname -s)" in
        Darwin)
            echo "macOS detected"
            check_command brew || { echo "Install Homebrew first" >&2; exit 1; }
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
}
```

### Multi-Platform Builds

```bash
cmd_build_all() {
    local platforms=(
        "darwin/amd64"
        "darwin/arm64"
        "linux/amd64"
        "linux/arm64"
    )

    for platform in "${platforms[@]}"; do
        IFS='/' read -r os arch <<< "$platform"
        output="bin/myapp-${os}-${arch}"
        echo "Building ${output}..."
        GOOS=$os GOARCH=$arch go build -o "$output" ./cmd/myapp
    done
}
```

### Shell Completions (Install/Uninstall)

```bash
cmd_install_completions() {
    local comp_dir
    case "$(uname -s)" in
        Darwin) comp_dir="$(brew --prefix)/share/zsh/site-functions" ;;
        Linux)  comp_dir="${HOME}/.local/share/zsh/site-functions" ;;
    esac
    mkdir -p "$comp_dir"
    myapp completion zsh > "${comp_dir}/_myapp"
    echo "Installed zsh completions to ${comp_dir}/_myapp"
}

cmd_uninstall_completions() {
    local comp_dir
    case "$(uname -s)" in
        Darwin) comp_dir="$(brew --prefix)/share/zsh/site-functions" ;;
        Linux)  comp_dir="${HOME}/.local/share/zsh/site-functions" ;;
    esac
    rm -f "${comp_dir}/_myapp"
    echo "Removed zsh completions"
}
```

## Separation of Concerns

| Belongs in Makefile       | Belongs in CLI           |
|---------------------------|--------------------------|
| Build / compile           | Daemon management        |
| Test execution            | Runtime configuration    |
| Code quality (lint, fmt)  | Version / info queries   |
| Dependency management     | Service lifecycle        |
| Install (initial setup)   | User-facing operations   |
| Development workflow      |                          |
| Cleanup                   |                          |

## Warden Application Example

### Full Target Reference

| Category        | Target                | Description                           |
|-----------------|-----------------------|---------------------------------------|
| **General**     | `help`                | Display organized help message        |
|                 | `prereqs`             | Check/install prerequisites (OS-aware)|
| **Build**       | `build`               | Build CLI + hostserver (debug mode)   |
|                 | `build-production`    | Cross-compile release binaries        |
|                 | `build-daemons`       | Build daemons for all platforms       |
|                 | `proto`               | Generate Go code from protobuf        |
| **Test**        | `test`                | Run all tests                         |
|                 | `test-unit`           | Unit tests only (fast)                |
|                 | `test-race`           | Unit tests with race detector         |
|                 | `test-integration`    | Integration tests                     |
|                 | `test-docker`         | Docker provider tests                 |
|                 | `test-e2e`            | End-to-end workflow tests             |
|                 | `test-coverage`       | Tests with coverage report            |
|                 | `test-coverage-html`  | Generate HTML coverage report         |
|                 | `test-pkg`            | Run tests for specific package        |
| **Quality**     | `lint`                | Run golangci-lint                     |
|                 | `fmt`                 | Format code with gofmt/goimports      |
|                 | `check`               | Run lint + test together              |
| **Install**     | `install`             | Alias for install-dev                 |
|                 | `install-dev`         | Build and install as symlinks         |
|                 | `install-production`  | Build and install as copies           |
|                 | `uninstall`           | Remove all installed files            |
| **Development** | `dev`                 | fmt -> test -> build workflow          |
|                 | `cycle`               | uninstall -> clean -> build -> install |
|                 | `run`                 | Run development version               |
|                 | `watch`               | Watch for changes and rebuild         |
| **Cleanup**     | `clean`               | Remove build artifacts                |
|                 | `clean-all`           | Deep clean including vendor           |

### Common Workflows

**First-time setup:**
```bash
make prereqs          # Check environment
make build            # Build debug binaries
make install          # Install to ~/.local/bin (symlinks)
warden daemon install # Register hostserver service
warden daemon start   # Start hostserver
```

**Daily development:**
```bash
make dev              # Format, test, build
# or
make cycle            # Full clean rebuild + install
```

**Running tests:**
```bash
make test             # All tests
make test-unit        # Fast unit tests only
make test-pkg PKG=internal/config  # Single package
make test-docker      # Docker integration tests
```

**Release build:**
```bash
make build-production # Cross-compile for all platforms
```

### Delegated to CLI (Not in Makefile)

```bash
# Daemon lifecycle
warden daemon status              # Show daemon status
warden daemon start               # Start daemon
warden daemon stop                # Stop daemon
warden daemon restart             # Restart daemon
warden daemon logs                # View logs
warden daemon logs -f             # Follow logs
warden daemon install             # Register as system service
warden daemon uninstall           # Remove system service

# Information
warden version                    # Show version info
```

### makehelp.sh Functions (Warden)

| Function                   | Purpose                                        |
|----------------------------|------------------------------------------------|
| `cmd_prereqs`              | OS-aware prerequisite checking                 |
| `cmd_build_production`     | Cross-platform production builds with LDFLAGS  |
| `cmd_build_daemons`        | Multi-platform daemon builds (CGO handling)    |
| `cmd_install_completions`  | Shell completion installation (bash, zsh, fish)|
| `cmd_uninstall_completions`| Shell completion removal                       |
| `cmd_install_cellserver`   | Install cellserver payloads to share directory |
| `cmd_install_snippets`     | Install snippet templates to share directory   |
