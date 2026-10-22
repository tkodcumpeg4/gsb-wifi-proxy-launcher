# embedded/

Files baked into `GSB_Gateway.exe` via `//go:embed`:

- `list.txt`    — the proxy list (committed; public data).
- `gsb-core.exe` — the Rust data plane. **Committed empty**; `../../build.ps1`
  copies the freshly built binary here before `go build`, producing the
  single-file executable. An empty placeholder lets `go build` compile; at
  runtime the app then falls back to a `gsb-core.exe` next to the exe.
- `wintun.dll`  — the WireGuard-signed TUN driver. Same scheme as above.
- `gsb-core`    — the Linux build of the data plane (static musl). Committed
  empty as well; `../../build-linux.sh` fills it.

A plain `go build` yields a runnable panel but no bundled TUN engine; run
`build.ps1` for the real single-file build.
