#!/usr/bin/env bash
# darkfactory-bringup.sh — launch (or reconcile) an Incus VM provisioned for
# kilroy development. Idempotent: re-running on an existing instance just
# verifies/installs missing pieces.
#
# Usage:
#   ./darkfactory-bringup.sh [--name NAME] [--type C<cpu>-M<mem-gb>] \
#                            [--disk SIZE] [--remote REMOTE:] [--image IMAGE]
#
# Defaults:
#   --name kilroyfactor   --type C8-M32   --disk 300GiB
#   --remote IncusOS:     --image images:ubuntu/noble
#
# Examples:
#   ./darkfactory-bringup.sh
#   ./darkfactory-bringup.sh --name df-austin --type C16-M64 --disk 500GiB
#   ./darkfactory-bringup.sh --remote H91:    # different incus host

set -euo pipefail

# =============================================================================
# CONFIG — extend lists here to add packages / toolchains.
# =============================================================================

# Apt packages installed unconditionally (compilers + dev essentials).
APT_PACKAGES=(
    # build essentials (C / C++)
    build-essential pkg-config gcc g++ make cmake autoconf automake libtool
    # cross-compilation toolchains commonly used by kilroy targets
    gcc-aarch64-linux-gnu libc6-dev-arm64-cross
    # general dev tools
    ca-certificates curl wget git jq vim less rsync openssh-client
    # observability / debugging
    htop iotop strace lsof tmux
    # libraries kilroy / izOS crates link against
    libssl-dev libtss2-dev libnftnl-dev libudev-dev
    # image / partition / fs tools (so this VM can host izOS image builds too)
    cpio gzip xz-utils parted gdisk dosfstools e2fsprogs squashfs-tools kpartx
    # qemu / swtpm for end-to-end boot tests
    qemu-system-x86 qemu-system-arm qemu-utils ovmf swtpm swtpm-tools tpm2-tools
    # signing / OCI / schema
    skopeo protobuf-compiler diffoscope
    # python (kilroy hooks)
    python3 python3-pip python3-venv
    # ADD MORE PACKAGES HERE ---------------------------------------------------
)

# Toolchain versions (set to "" to skip a step).
GO_VERSION="1.25.0"            # kilroy's go.mod requires >= 1.25
RUST_VERSION="1.83.0"          # matches kilroy worktree rust-toolchain.toml
COSIGN_VERSION="v2.4.1"        # signing tool used by izOS build
ZIG_VERSION="0.13.0"           # cargo-zigbuild's runtime dep
CARGO_ZIGBUILD_VERSION="0.20.1"  # MSRV-compat with Rust 1.83

# Kilroy source
KILROY_REPO="https://github.com/x85446/kilroy.git"
KILROY_BRANCH="bug-89-fix-with-flag"
KILROY_INSTALL_PREFIX="/usr/local/bin"

# =============================================================================
# Defaults
# =============================================================================

DEFAULT_NAME="kilroyfactor"
DEFAULT_TYPE="C8-M32"
DEFAULT_DISK="300GiB"
DEFAULT_REMOTE="IncusOS:"
DEFAULT_IMAGE="images:ubuntu/noble"

# =============================================================================
# Arg parse
# =============================================================================

NAME="$DEFAULT_NAME"
TYPE="$DEFAULT_TYPE"
DISK="$DEFAULT_DISK"
REMOTE="$DEFAULT_REMOTE"
IMAGE="$DEFAULT_IMAGE"

usage() { sed -n '2,18p' "$0" | sed 's|^# \{0,1\}||'; }

while [ $# -gt 0 ]; do
    case "$1" in
        --name)   NAME="$2"; shift 2 ;;
        --type)   TYPE="$2"; shift 2 ;;
        --disk)   DISK="$2"; shift 2 ;;
        --remote) REMOTE="$2"; shift 2 ;;
        --image)  IMAGE="$2"; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "unknown arg: $1" >&2; usage >&2; exit 2 ;;
    esac
done

# Parse C8-M32 → CPU=8, MEM=32
CPU="$(printf '%s' "$TYPE"   | sed -nE 's/^[Cc]([0-9]+)-[Mm][0-9]+$/\1/p')"
MEM_GB="$(printf '%s' "$TYPE" | sed -nE 's/^[Cc][0-9]+-[Mm]([0-9]+)$/\1/p')"
if [ -z "$CPU" ] || [ -z "$MEM_GB" ]; then
    echo "invalid --type: $TYPE (expected C<cpu>-M<mem-gb>, e.g. C8-M32)" >&2
    exit 2
fi

# Disk: append GiB if pure digits
case "$DISK" in
    *[!0-9]*) ;;                # already has a suffix
    *) DISK="${DISK}GiB" ;;
esac

INST="${REMOTE%:}:${NAME}"
[ "$REMOTE" = "${REMOTE%:}" ] && INST="$REMOTE:$NAME"

echo "==> target: $INST  cpu=$CPU  mem=${MEM_GB}GiB  disk=$DISK  image=$IMAGE"

# =============================================================================
# Phase 1 — launch VM (or reconcile if it exists)
# =============================================================================

if incus info "$INST" >/dev/null 2>&1; then
    echo "==> instance already exists; reconciling install steps"
    # incus 7+ requires `incus list <remote>: <filter>` (two args).
    STATE="$(incus list "${REMOTE%:}:" "$NAME" --format csv -c s 2>/dev/null)"
    if [ "$STATE" != "RUNNING" ]; then
        echo "==> starting $INST"
        incus start "$INST"
    fi
else
    echo "==> launching $INST as VM"
    incus launch "$IMAGE" "$INST" --vm \
        -c limits.cpu="$CPU" \
        -c limits.memory="${MEM_GB}GiB" \
        -d root,size="$DISK"
fi

# Wait for incus-agent so we can `incus exec`.
echo "==> waiting for incus-agent (VM boot + cloud-init)"
for i in $(seq 1 60); do
    if incus exec "$INST" -- true 2>/dev/null; then
        echo "==> agent up ($((i*5))s)"
        break
    fi
    sleep 5
done
incus exec "$INST" -- true || { echo "agent never came up" >&2; exit 1; }

IP="$(incus list "${REMOTE%:}:" "$NAME" --format csv -c 4 2>/dev/null | awk 'NR==1{print $1}')"
echo "==> IP: ${IP:-<not yet assigned>}"

# =============================================================================
# Phase 2 — provision (run remote install script via incus exec)
# =============================================================================

# Compose the remote install script. Heredoc, single-quoted to avoid local
# expansion; placeholders substituted explicitly below where needed.
REMOTE_SCRIPT="$(cat <<REMOTE_EOF
#!/usr/bin/env bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
PATH=/usr/local/go/bin:/root/.cargo/bin:\$PATH

step() { printf '\n--- %s ---\n' "\$*"; }

step "apt: update + install $(echo "${APT_PACKAGES[@]}" | wc -w) packages"
apt-get update -q
apt-get install -y --no-install-recommends ${APT_PACKAGES[@]}

if [ -n "${GO_VERSION}" ]; then
  step "go: install ${GO_VERSION}"
  WANT="go${GO_VERSION}"
  # || true: protect against go not yet installed (set -e + pipefail would otherwise exit 127)
  HAVE="\$(/usr/local/go/bin/go version 2>/dev/null | awk '{print \$3}' || true)"
  if [ "\$HAVE" != "\$WANT" ]; then
      rm -rf /usr/local/go
      curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" | tar -xz -C /usr/local
      ln -sf /usr/local/go/bin/go    /usr/local/bin/go
      ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
  fi
  /usr/local/go/bin/go version
fi

if [ -n "${RUST_VERSION}" ]; then
  step "rust: install ${RUST_VERSION} + cross targets"
  if ! [ -x /root/.cargo/bin/rustc ]; then
      curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs \
          | sh -s -- -y --default-toolchain "${RUST_VERSION}" --profile minimal
  else
      /root/.cargo/bin/rustup default "${RUST_VERSION}"
  fi
  /root/.cargo/bin/rustup target add \
      x86_64-unknown-linux-musl \
      aarch64-unknown-linux-musl \
      x86_64-unknown-linux-gnu \
      aarch64-unknown-linux-gnu
  /root/.cargo/bin/rustup component add rustfmt clippy llvm-tools-preview
  /root/.cargo/bin/rustc --version
fi

if [ -n "${CARGO_ZIGBUILD_VERSION}" ] && [ -n "${RUST_VERSION}" ]; then
  step "cargo-zigbuild ${CARGO_ZIGBUILD_VERSION}"
  if ! [ -x /root/.cargo/bin/cargo-zigbuild ]; then
      /root/.cargo/bin/cargo install --locked --version "${CARGO_ZIGBUILD_VERSION}" cargo-zigbuild
  fi
  /root/.cargo/bin/cargo-zigbuild --version
fi

if [ -n "${ZIG_VERSION}" ]; then
  step "zig ${ZIG_VERSION}"
  if ! [ -x /usr/local/bin/zig ] || [ "\$(zig version 2>/dev/null)" != "${ZIG_VERSION}" ]; then
      curl -fsSL -o /tmp/zig.tar.xz "https://ziglang.org/download/${ZIG_VERSION}/zig-linux-x86_64-${ZIG_VERSION}.tar.xz"
      rm -rf "/opt/zig-linux-x86_64-${ZIG_VERSION}"
      tar -xJf /tmp/zig.tar.xz -C /opt/
      ln -sf "/opt/zig-linux-x86_64-${ZIG_VERSION}/zig" /usr/local/bin/zig
      rm -f /tmp/zig.tar.xz
  fi
  zig version
fi

if [ -n "${COSIGN_VERSION}" ]; then
  step "cosign ${COSIGN_VERSION}"
  if ! [ -x /usr/local/bin/cosign ]; then
      curl -fsSL -o /usr/local/bin/cosign \
          "https://github.com/sigstore/cosign/releases/download/${COSIGN_VERSION}/cosign-linux-amd64"
      chmod +x /usr/local/bin/cosign
  fi
  cosign version | head -3
fi

if [ -n "${KILROY_REPO}" ]; then
  step "kilroy: clone + build + install"
  SRC=/root/projects/kilroy
  mkdir -p /root/projects
  if [ -d "\$SRC/.git" ]; then
      git -C "\$SRC" fetch origin --prune
      git -C "\$SRC" checkout "${KILROY_BRANCH}"
      git -C "\$SRC" reset --hard "origin/${KILROY_BRANCH}"
  else
      git clone --branch "${KILROY_BRANCH}" "${KILROY_REPO}" "\$SRC"
  fi
  cd "\$SRC"
  go build -o /tmp/kilroy.new ./cmd/kilroy
  install -m 755 /tmp/kilroy.new "${KILROY_INSTALL_PREFIX}/kilroy"
  rm -f /tmp/kilroy.new
  echo "kilroy installed at ${KILROY_INSTALL_PREFIX}/kilroy"
  # kilroy has no --version; show the source commit instead
  echo -n "kilroy source: "
  git -C "\$SRC" log --oneline -1

fi

step "done"
REMOTE_EOF
)"

echo "==> phase 2: provisioning (this can take several minutes on first run)"
incus exec "$INST" -- bash -c "$REMOTE_SCRIPT"

# =============================================================================
# Phase 2.5 — deploy helper scripts from local checkout
# =============================================================================
# These ride along with this script in the same scripts/ directory. We push
# them from local rather than relying on the in-VM git clone, so that local
# edits to the helpers land on the VM even before they're pushed upstream.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
echo "==> phase 2.5: deploying helper scripts from $SCRIPT_DIR to /opt/darkfactory/scripts/"
incus exec "$INST" -- mkdir -p /opt/darkfactory/scripts
deployed=0
for f in "$SCRIPT_DIR"/kilroy-*.sh "$SCRIPT_DIR"/darkfactory-bringup.sh; do
    [ -f "$f" ] || continue
    incus file push --quiet --mode=0755 "$f" "$INST/opt/darkfactory/scripts/$(basename "$f")"
    deployed=$((deployed + 1))
done
echo "==> deployed $deployed helper script(s)"
incus exec "$INST" -- ls -1 /opt/darkfactory/scripts/

# =============================================================================
# Phase 3 — summary
# =============================================================================

cat <<EOF

==============================================================================
$INST is ready.

  Connect:  incus exec $INST -- bash
  IP:       ${IP:-<see: incus list $INST>}
  CPU:      $CPU
  Memory:   ${MEM_GB}GiB
  Disk:     $DISK
  Image:    $IMAGE

Re-run this script any time to reconcile / install new packages added to
APT_PACKAGES at the top of $(basename "$0").
==============================================================================
EOF
