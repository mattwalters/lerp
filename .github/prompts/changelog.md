# Draft CHANGELOG.md Section

Draft the next version section for `CHANGELOG.md` based on changes between the last release tag and `origin/main`.

## Range

1. Find the last release tag using:
   ```bash
   git describe --tags --abbrev=0
   ```
2. Inspect the commits and diffs in the range `<last-tag>..origin/main`.

## Compatibility Surface

Semver for this repository reflects the user-facing compatibility surface, not internal Go APIs (which are under `internal/` and unimportable).

Read the **diff** of the compat surface paths, not just commit subjects:

| Surface | Paths |
| -- | -- |
| Commands, flags | `cmd/lerp/main.go` |
| Config keys, defaults | `internal/config/config.go`, `internal/config/format.go`, `internal/config/stock.toml` |
| State layout | `internal/evidence/` (`.lerp/`) |
| Credentials on disk | `internal/credentials/` |
| Install and update contract | `install.sh`, `.goreleaser.yaml`, `internal/update/` |
| Documented promises | `README.md`, `docs/` |

Commit subjects can be misleading. For example, `install.sh` hardcodes `lerp_${version}_${os}_${arch}.tar.gz`, which must match `name_template` in `.goreleaser.yaml`. Because `install.sh` can install older versions, changing `name_template` breaks installing previously shipped releases. A commit titled "tidy up archive naming" might read like a chore in the commit log, but its diff reveals a breaking change.

## Scope Filter

Follow the guidance in `CHANGELOG.md`: the changelog covers the binary, not website documentation or marketing material. Commits affecting only `lerp.sh` / the docs site do not produce changelog entries unless they affect the binary or documented user promises.

## Version Bump Convention

This project is in `0.x` semver:
- `0.MINOR.0` for new features or breaking changes.
- `0.0.PATCH` for bug fixes and minor improvements.

## Instructions

1. Run `git describe --tags --abbrev=0` and examine the changes up to `origin/main` with `git diff` and `git log`.
2. Determine the proposed version bump (`0.MINOR.0` or `0.0.PATCH`) and provide a brief rationale explaining the bump choice.
3. Format the new section under `## [Unreleased]` in `CHANGELOG.md` using the format:
   ```markdown
   ## [X.Y.Z] — YYYY-MM-DD
   ```
   Use Keep a Changelog categories (`### Added`, `### Changed`, `### Deprecated`, `### Removed`, `### Fixed`, `### Security`) and match the punctuation and style of existing entries (e.g. em-dash `—` between version and date).
4. State the date you used from your context for the heading so the operator can verify it (the tool grant cannot run `date`).
5. Update `CHANGELOG.md` directly using Edit.
6. Present the proposed bump, rationale, and drafted section to the operator for review and discussion.
