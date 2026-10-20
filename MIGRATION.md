# craft-libs: Rust → Go migration plan

> **Status:** C0 done (Go module, shared gates); C1 next · **Created:** 2026-10-20 · **Rust baseline:** `ae2f796` (computernut43/craft-libs `main`)
>
> Companion to [`computernut43/wordcraft` MIGRATION.md](https://github.com/computernut43/wordcraft/blob/main/MIGRATION.md),
> which sets the rules this plan follows: port one small, tested component at a time; don't remove Rust until its Go
> replacement builds, passes its tests and has been reviewed; claim parity only after checking against the Rust code.

## 1. Why craft-libs is ported too

WordCraft's Rust build depends on three craft-libs crates, pinned by git revision:

| WordCraft uses | Pinned revision | What for | Where in WordCraft |
|---|---|---|---|
| `craft-pdf` (feature `write`) and, through `craft_pdf::write::backend`, **krilla** directly | `c348732` (50 commits behind the baseline) | All PDF export: `Document`, pages, `Surface` (paths, fills, strokes, linear gradients, text with embedded fonts), images, outline, link annotations and destinations, actions, tagging, metadata, PDF version/archival configuration, the `guard` panic boundary | `crates/pdf` |
| `craft-ui` | `d27646a` (106 commits behind) | `printing::Labels` and the print-panel controls | `crates/ui-egui/src/printing.rs` |
| `craft-print` | `d27646a` | `printers`, `submit`, `Job`, `Duplex`, `Scaling` | `apps/wordcraft` |

The Go WordCraft needs Go versions of these. The other crates (AI core, atomic saves, colour management, document
paths, i18n, quit, settings) are shared by the Craft suite and are ported afterwards, so the whole library moves to Go.

**Licence:** the workspace is `MIT OR Apache-2.0` (`Cargo.toml`, `LICENSE-*`). The vendored backends krilla and hayro
are third-party, also `MIT OR Apache-2.0` (each `vendor/*/Cargo.toml`), with notices in `licenses/` and the snapshot
history in `vendor/README.craft-reader.md`. A Go port of them is a derivative work. It keeps their copyright notices
(`licenses/` stays, and each Go package that ports one names its source and revision), which both licences allow.

## 2. Audit

~31.4k lines in `crates/` (560 `#[test]`s) plus ~81.4k vendored lines in `vendor/`.

| Crate | Lines | Tests | Responsibility | Rust deps | Used by WordCraft |
|---|---|---|---|---|---|
| `ai-core` | 6.9k | 215 | AI connections, roles, requests, jobs, consent; no I/O; wasm-safe; mock provider | serde, serde_json | no |
| `atomic` | 1.5k | 33 | Crash-safe atomic file replacement (temp file in same dir, fsync, rename, dir fsync) | rustix (Apple) | no (WordCraft has its own `safe_save`) |
| `cms` | 6.7k | 67 | ICC v2/v4 colour management: parse/write profiles, transforms, intents, black point compensation | rayon | no |
| `docpath` | 1.0k | 12 | Decide whether a path/URL stored in a document may be followed (local, same share; never remote/device) | none | no |
| `i18n` | 0.9k | 35 | Embedded catalogs, lookup with fallback, plurals, language names, system locale | intl_pluralrules, unic-langid, sys-locale | no |
| `pdf` | 2.5k | 5 | Feature-gated PDF reading (hayro-syntax), interpreting, rasterising (hayro), writing (krilla) | hayro*, krilla, bytemuck | **write only** |
| `print` | 3.6k | 48 | Spooler: CUPS via `lpstat`/`lp`/`lpoptions` on macOS and Linux; Windows spooler + `Windows.Data.Pdf` + XPS PrintTicket in-process | storytold/winprint.rs (git), winsafe, roxmltree, miniz_oxide, tempfile | **yes** |
| `quit` | 0.4k | 9 | Quit the macOS way (`-[NSApplication terminate:]`) to avoid an eframe/AppKit crash | objc2, objc2-app-kit, dispatch2, muda | no (WordCraft has its own) |
| `settings` | 2.0k | 50 | `config.toml` location, never-failing load, atomic save, backup, damaged-file recovery, migrations, **comment-preserving** merges | craft-atomic, serde, toml_edit, dirs | no |
| `ui` | 6.1k | 86 | egui components: buttons, tabs, docking, icons, layout, theme, printing panel | egui | **printing panel** |
| `vendor/krilla` | 24.7k | 21 | PDF writer (patched: unwind poisoning) | many | yes, through `craft-pdf` |
| `vendor/hayro*` | 56.7k | 316 | PDF reader, interpreter, rasteriser, JBIG2 decoder (patched snapshot) | many | no |

## 3. Go layout

Same approach as WordCraft: **this repository, a Go module at the root, Rust frozen beside it until cutover.**

```
go.mod                       module github.com/computernut43/craft-libs
devtools/
  nopanic/                   go/analysis pass: no explicit panic / Must* in non-test code (shared by all Craft Go repos)
  gates/                     shared quality gates: gofmt, vet, staticcheck, tests, nopanic, wasm, licences, govulncheck
tools/ci/                    this repository's gate runner (uses devtools/gates)
aicore/                      crates/ai-core
atomic/                      crates/atomic
cms/                         crates/cms
docpath/                     crates/docpath
i18n/                        crates/i18n
pdf/
  write/                     craft-pdf `write` + the krilla subset WordCraft uses
  read/  interpret/  raster/ craft-pdf `read`/`interpret`/`raster` + hayro (later milestone)
print/                       crates/print
quit/                        crates/quit (re-evaluated against the Go UI toolkit)
settings/                    crates/settings
ui/                          crates/ui (rewritten on the toolkit WordCraft's UI spike picks)
```

Packages are public (no `internal/`), because other modules (WordCraft and the other Craft apps) import them.
WordCraft depends on a pushed commit of this module (a Go pseudo-version) and never on a local path. Cross-repo
development uses an uncommitted `go.work`.

## 4. Library decisions (checked 2026-10-20 on the Go module proxy and in source)

| Need (Rust) | Go | Notes |
|---|---|---|
| Static analysis for the never-crash rule | `golang.org/x/tools/go/analysis` v0.52.0 (BSD) | Used by `devtools/nopanic` |
| serde/serde_json (`ai-core`, `settings`) | `encoding/json` | |
| Plural rules (`intl_pluralrules`), language ids (`unic-langid`) | `golang.org/x/text/feature/plural`, `golang.org/x/text/language` | CLDR-based. Checked against the Rust tests when `i18n` is ported |
| System locale (`sys-locale`) | Own: `LANG`/`LC_*` on Unix, `purego` → `GetUserDefaultLocaleName` on Windows, `NSLocale` via `purego/objc` on macOS, `navigator.languages` via `syscall/js` | |
| Format-preserving TOML (`toml_edit`) | **No Go equivalent found.** `pelletier/go-toml/v2` v2.4.3 parses and encodes but doesn't keep comments | Spike when `settings` is ported: own minimal comment-preserving merge, or a `gap` the owner accepts |
| Dirs (`dirs`) | `os.UserConfigDir` + per-OS rules from the Rust code | |
| fsync / rename (`rustix`) | `os`, `golang.org/x/sys/unix` (`F_FULLFSYNC` on Apple) | |
| Rayon (`cms`) | goroutines | |
| PDF writing (krilla) | Own `pdf/write` covering exactly the krilla API WordCraft uses (§1). Font parsing via `github.com/go-text/typesetting`. Subsetting is ours to write: go-text's `font/opentype` has a table writer (`writer.go`), but whether it can produce subsets is checked when C2 starts | The biggest item. Ported from krilla's behaviour, with its notice kept |
| PDF reading/rasterising (hayro) | Own port in `pdf/read`, `pdf/interpret`, `pdf/raster` | ~57k lines, not needed by WordCraft; scheduled last |
| CUPS (`lpstat`, `lp`) | `os/exec`, same commands, no shell | Straight port |
| Windows printing (winprint.rs, winsafe, WinRT `Windows.Data.Pdf`, XPS) | `golang.org/x/sys/windows` + `purego`/COM for WinRT | Spike: WinRT from Go without cgo. `storytold/winprint.rs` isn't in this session's scope; it's ported from its public behaviour, or reimplemented |
| egui (`ui`) | WordCraft's UI-spike toolkit (Gio leading) | Rewrite, not translation |

## 5. Parity checklist

Status: `not started` · `in progress` · `ported` · `verified` · `gap`.

| Component | Rust | Go | Status |
|---|---|---|---|
| Shared never-crash analyzer | (clippy lints) | `devtools/nopanic` | done (C0): new, not a port; tests in `devtools/nopanic` |
| Shared gates | per-crate workflows | `devtools/gates`, `tools/ci`, `.github/workflows/go.yml` | done (C0) |
| Atomic save | `crates/atomic` | `atomic` | not started |
| Document paths | `crates/docpath` | `docpath` | not started |
| i18n | `crates/i18n` | `i18n` | not started |
| Settings | `crates/settings` | `settings` | not started |
| PDF write: configuration, guard, font limits | `crates/pdf/src/write.rs` | `pdf/write` | not started |
| PDF write: krilla subset (document, page, surface, paths, gradients, text, images, outline, annotations, tagging, metadata) | `vendor/krilla` | `pdf/write` | not started |
| Printing: CUPS | `crates/print/src/lib.rs` | `print` | not started |
| Printing: print-ready PDF sheets | `crates/print/src/sheets.rs` | `print` | not started |
| Printing: Windows | `crates/print/src/{windows,ticket}.rs` | `print` | not started |
| UI controls (printing panel first) | `crates/ui` | `ui` | not started |
| AI core | `crates/ai-core` | `aicore` | not started |
| Colour management | `crates/cms` | `cms` | not started |
| Quit | `crates/quit` | `quit` | not started |
| PDF read/interpret/raster | `crates/pdf` + `vendor/hayro*` | `pdf/read` … | not started |

## 6. Milestones

Ordered by what the Go WordCraft needs first (its milestones in brackets).

| # | Milestone | Needed by WordCraft |
|---|---|---|
| C0 | Go module, `devtools/nopanic`, `devtools/gates`, `tools/ci`, CI workflow | WordCraft M0 (its gate runner imports `devtools`) |
| C1 | `atomic`, `docpath` (small, pure; prove the porting conventions) | WordCraft M4 (safe save, linked files) can reuse them |
| C2 | `pdf/write` (configuration, guard, then the krilla subset) | WordCraft M8 |
| C3 | `i18n`, `settings` | WordCraft M9 |
| C4 | `print` (CUPS, sheets, then Windows) | WordCraft M10 |
| C5 | `ui` (printing panel first) on the chosen toolkit | WordCraft M9/M10 |
| C6 | `aicore`, `cms`, `quit` | Other Craft apps |
| C7 | `pdf/read`, `pdf/interpret`, `pdf/raster` (hayro) | Other Craft apps (PdfCraft) |
| C8 | Cutover: tag `rust-final`, remove the Rust workspace | |

## 7. Risks

| Risk | Mitigation |
|---|---|
| WordCraft pins older craft-libs revisions than the baseline; APIs moved since (`git diff --stat c348732 HEAD -- crates/pdf vendor/krilla`: 14 files, +2,553 lines) | The Go API is designed for the Go WordCraft's needs and checked against both WordCraft's call sites and the baseline tests |
| krilla's PDF output is the reference for tagging and fonts | Compare Go PDFs with Rust ones structurally (objects, structure tree, fonts, text extraction), not byte for byte |
| No comment-preserving TOML editor in Go | Spike in C3; documented gap if dropped |
| Windows printing needs WinRT | Spike in C4; CUPS path first |
| hayro is ~57k lines | Last milestone; not on WordCraft's path |

## Revision history

| Date | Change | Summary |
|---|---|---|
| 2026-10-20 | minor | C0 landed: Go module, `devtools/nopanic`, `devtools/gates`, `tools/ci`, Go workflow; `-mod=mod` while Rust `vendor/` exists |
| 2026-10-20 | major | First version: audit, Go layout, library decisions, checklist, milestones |
