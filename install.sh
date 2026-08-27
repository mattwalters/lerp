#!/bin/sh
# One-line install for people without a Go toolchain: downloads the release
# archive matching this machine's OS and arch from GitHub releases, verifies
# it against the published checksums, and unpacks the binary into a bin dir
# on no one's PATH but the user's own — no sudo, no shell profile edited.
#
#   curl -fsSL https://raw.githubusercontent.com/mattwalters/lerp/main/install.sh | sh
#
# `go install github.com/mattwalters/lerp/cmd/lerp@latest` stays the
# documented path for anyone with a Go toolchain already; this is the other
# one, for anyone who doesn't want to install Go just to get one binary.
set -eu

repo="mattwalters/lerp"
bin_dir="$HOME/.local/bin"

usage() {
  cat <<EOF
Usage: install.sh [--bin-dir DIR]

Installs the lerp binary built for this machine's OS and architecture from
the latest GitHub release. Defaults to \$HOME/.local/bin; pass --bin-dir to
choose another directory. Never uses sudo and never edits your PATH.

Piped from curl, flags go after -s --:
  curl -fsSL .../install.sh | sh -s -- --bin-dir DIR
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --bin-dir)
      [ $# -ge 2 ] || { echo "install.sh: --bin-dir needs a directory" >&2; exit 1; }
      bin_dir=$2
      shift 2
      ;;
    --bin-dir=*)
      bin_dir=${1#--bin-dir=}
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      echo "install.sh: unknown argument: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

# What lerp builds for (see .goreleaser.yaml): the loop holds an advisory
# flock, starts agents in their own process groups and reaps by killing the
# group, none of which Windows has as such.
case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *)
    echo "install.sh: unsupported OS $(uname -s) — lerp runs on macOS and Linux" >&2
    exit 1
    ;;
esac

case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *)
    echo "install.sh: unsupported architecture $(uname -m) — lerp builds for amd64 and arm64" >&2
    exit 1
    ;;
esac

# Resolving the redirect a browser would follow avoids both a JSON parser and
# the GitHub API's much tighter unauthenticated rate limit — this is a plain
# object fetch, not an API call. A repo with no releases yet does not 404
# here: GitHub 200s /releases/latest and redirects it to the plain /releases
# page instead of a tag page, so the tag is read from the redirect's shape,
# not just checked for being empty.
latest_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' \
  "https://github.com/$repo/releases/latest") || {
  echo "install.sh: could not reach GitHub to resolve the latest release" >&2
  exit 1
}
case "$latest_url" in
  */releases/tag/*) tag=${latest_url##*/} ;;
  *)
    echo "install.sh: no published release found for $repo — has one been tagged yet?" >&2
    exit 1
    ;;
esac
version=${tag#v}

archive="lerp_${version}_${os}_${arch}.tar.gz"
base_url="https://github.com/$repo/releases/download/$tag"

workdir=$(mktemp -d)
tmp_bin="$bin_dir/.lerp.tmp.$$"
trap 'rm -rf "$workdir"; rm -f "$tmp_bin"' EXIT

echo "install.sh: downloading $archive ($tag)"
curl -fsSL -o "$workdir/$archive" "$base_url/$archive"
curl -fsSL -o "$workdir/checksums.txt" "$base_url/checksums.txt"

checksum_line=$(grep -F "  $archive" "$workdir/checksums.txt") || {
  echo "install.sh: $archive is not listed in checksums.txt" >&2
  exit 1
}

if command -v sha256sum >/dev/null 2>&1; then
  ( cd "$workdir" && printf '%s\n' "$checksum_line" | sha256sum -c - >/dev/null )
elif command -v shasum >/dev/null 2>&1; then
  ( cd "$workdir" && printf '%s\n' "$checksum_line" | shasum -a 256 -c - >/dev/null )
else
  echo "install.sh: need sha256sum or shasum to verify the download" >&2
  exit 1
fi

# Extracted straight to a temp file beside the target and renamed into place,
# not via $workdir: $workdir sits under $TMPDIR, often a different filesystem
# than $bin_dir, and mv falls back to copy across a filesystem boundary — on
# Linux that copy can hit ETXTBSY reopening a lerp that is currently running,
# e.g. re-running the installer to upgrade while the board is open elsewhere.
# A same-directory rename cannot cross a filesystem, so it cannot land that
# way half-written either.
mkdir -p "$bin_dir"
tar -xzf "$workdir/$archive" -O lerp > "$tmp_bin"
chmod 755 "$tmp_bin"
mv "$tmp_bin" "$bin_dir/lerp"

printf 'installed %s to %s\n' "$tag" "$bin_dir/lerp"
case ":$PATH:" in
  *":$bin_dir:"*)
    printf 'PATH resolves lerp to: %s\n' "$(command -v lerp || echo "$bin_dir/lerp")"
    ;;
  *)
    # $PATH here is literal — the line shown is something to paste, not
    # something this script should expand.
    # shellcheck disable=SC2016
    printf '%s is not on your PATH — add it, e.g.: export PATH="%s:$PATH"\n' "$bin_dir" "$bin_dir"
    ;;
esac
