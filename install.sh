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
# object fetch, not an API call.
latest_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' \
  "https://github.com/$repo/releases/latest")
tag=${latest_url##*/}
if [ -z "$tag" ] || [ "$tag" = "latest" ]; then
  echo "install.sh: could not resolve the latest release tag — is one published yet?" >&2
  exit 1
fi
version=${tag#v}

archive="lerp_${version}_${os}_${arch}.tar.gz"
base_url="https://github.com/$repo/releases/download/$tag"

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

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

tar -xzf "$workdir/$archive" -C "$workdir" lerp

mkdir -p "$bin_dir"
mv "$workdir/lerp" "$bin_dir/lerp"
chmod 755 "$bin_dir/lerp"

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
