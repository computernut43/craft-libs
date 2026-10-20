# AGENTS.md

Shared Rust libraries for the Craft Apps. The shared standards live in
[craftrules](https://github.com/storytold/craftrules) (`../../craftrules` on a dev machine): read its
`AGENTS.md`, then `standards/never-crash.md`, `standards/engineering.md` and `standards/licensing.md`.

- **Never crash.** Every crate's `lib.rs` starts with
  `#![deny(clippy::unwrap_used, clippy::expect_used, clippy::panic, clippy::unimplemented, clippy::todo, clippy::unreachable)]`.
  No `unsafe` (the workspace forbids it).
- **New crates** go in `crates/<name>`, named `craft-<name>`, inherit the workspace package fields
  and lints, and are listed in `[workspace.dependencies]`.
- **No app code.** A crate here must be useful to more than one app and must not depend on any app.
- **Before merging:** `cargo fmt --check`, `cargo clippy --workspace --all-targets -- -D warnings`,
  `cargo test --workspace`.

## Go (migration in progress)

The libraries are being ported to Go; the plan and progress are in [`MIGRATION.md`](MIGRATION.md). Until the
cutover the Rust crates stay as they are and the Go module (`github.com/computernut43/craft-libs`, at the root)
grows beside them. The rules above carry over:

- **Never crash.** No `panic`, no `Must…` with non-constant arguments, no `log.Panic*`/`log.Fatal*`, no `os.Exit`
  outside `package main` in non-test code (`devtools/nopanic` enforces it). Errors are returned; input-derived
  numbers are bounds-checked; readers get fuzz tests.
- **Gates before every commit:** `go run -mod=mod ./tools/ci` (fmt, vet, nopanic, staticcheck, test, wasm,
  licences, vuln). Run one gate by name, e.g. `go run -mod=mod ./tools/ci test`.
- **`-mod=mod`:** the Rust third-party sources live in `vendor/`, which the go command would take for Go
  vendoring. `tools/ci` sets `GOFLAGS=-mod=mod`; set it yourself for other go commands here. Modules that
  depend on craft-libs are not affected.
- **Licences:** dependencies must be MIT, BSD, Apache-2.0, ISC, 0BSD, Unlicense or Zlib (`tools/ci licences`).
- **Porting a crate:** document the Rust behaviour and tests first, port the tests, then the code; update the
  checklist in `MIGRATION.md`. Don't remove Rust code before the Go replacement is reviewed.
- Local cross-repository work (e.g. with WordCraft) uses an uncommitted `go.work`.
