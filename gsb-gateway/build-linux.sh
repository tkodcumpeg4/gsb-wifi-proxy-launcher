#!/usr/bin/env bash
# Linux counterpart of build.ps1: builds the Rust data plane and the Go
# control plane for linux/amd64 and assembles a portable folder in
# ../dist-linux plus ../GSB_Gateway-linux-amd64.tar.gz. Needs go, cargo, the
# musl target and musl-gcc, so gsb-core is static and runs on any distro:
#   rustup target add x86_64-unknown-linux-musl; sudo apt install musl-tools
#   bash gsb-gateway/build-linux.sh
# CARGO_TARGET_DIR may point outside the tree (e.g. when the sources sit on a
# Windows drive under WSL, where building in place is slow).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
root="$(dirname "$here")"
dist="$root/dist-linux"
target="${CARGO_TARGET_DIR:-$here/core/target}"

cp "$root/ui.html" "$root/mini.html" "$here/control/web/"

musl=x86_64-unknown-linux-musl
(cd "$here/core" && cargo test --release --target-dir "$target" &&
  cargo build --release --target "$musl" --target-dir "$target")

# Stage files embedded into the single gsb-gateway binary.
mkdir -p "$here/control/embedded"
install -m 755 "$target/$musl/release/gsb-core" "$here/control/embedded/gsb-core"
cp "$root/list.txt" "$here/control/embedded/list.txt"

# CGO off: the tray talks D-Bus in pure Go, so the binary has no libc/GTK deps.
(cd "$here/control" && go test ./... &&
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o bin/gsb-gateway .)

(cd "$here/relay" && go test ./... &&
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o bin/gsb-relay-linux-amd64 .)

rm -rf "$dist"
mkdir -p "$dist/server"
install -m 755 "$here/control/bin/gsb-gateway" "$dist/gsb-gateway"
install -m 755 "$here/linux/install.sh" "$dist/install.sh"
cp "$here/linux/gsb-gateway.desktop" "$dist/"
cp "$here/control/tray.png" "$dist/gsb-gateway.png"
cp "$here/linux/BeniOku-Linux.txt" "$dist/BeniOku.txt"
install -m 755 "$here/relay/bin/gsb-relay-linux-amd64" "$dist/server/gsb-relay"
cp "$here/relay/gsb-relay.service" "$dist/server/"
echo "dist hazir: $dist"

tarball="$root/GSB_Gateway-linux-amd64.tar.gz"
rm -f "$tarball"
tar -C "$root" --owner=0 --group=0 --transform 's,^dist-linux,GSB_Gateway,' -czf "$tarball" dist-linux
echo "tar hazir: $tarball"
