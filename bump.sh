#!/usr/bin/env bash
set -euo pipefail

usage() {
  printf 'Usage: %s -v 1.0.1 [-p]\n' "$(basename "$0")" >&2
  exit 2
}

VERSION=""
PUSH=0

while getopts ":v:p" opt; do
  case "$opt" in
    v) VERSION="$OPTARG" ;;
    p) PUSH=1 ;;
    *) usage ;;
  esac
done
shift $((OPTIND - 1))

[[ $# -eq 0 ]] || usage
[[ -n "$VERSION" ]] || usage
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || {
  printf 'Version must use x.y.z format, got: %s\n' "$VERSION" >&2
  exit 2
}

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
  printf 'Tracked working tree is not clean. Commit or stash changes first.\n' >&2
  git status --short --untracked-files=no >&2
  exit 1
fi

# mygo.json is the one place the version lives: the build stamps it into the
# app, and the release workflow checks that the tag matches it.
CONFIG="mygo.json"
TAG="v$VERSION"
CURRENT_VERSION="$(perl -0ne 'print $1 if /^\s*"version"\s*:\s*"([^"]*)"/m' "$CONFIG")"

[[ -n "$CURRENT_VERSION" ]] || {
  printf 'No "version" in %s.\n' "$CONFIG" >&2
  exit 1
}

if [[ "$CURRENT_VERSION" == "$VERSION" ]]; then
  printf '%s is already at %s.\n' "$CONFIG" "$VERSION" >&2
  exit 1
fi

if git rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
  printf 'Local tag already exists: %s\n' "$TAG" >&2
  exit 1
fi

if git remote get-url origin >/dev/null 2>&1 &&
  git ls-remote --exit-code --tags origin "refs/tags/$TAG" >/dev/null 2>&1; then
  printf 'Remote tag already exists: %s\n' "$TAG" >&2
  exit 1
fi

VERSION="$VERSION" perl -0pi -e '
  $n = s{^(\s*"version"\s*:\s*")[^"]*(")}{$1$ENV{VERSION}$2}m;
  die "Missing version key\n" unless $n;
' "$CONFIG"

git add "$CONFIG"
git commit -m "chore(release): bump version to $VERSION"
git tag -a "$TAG" -m "Release $TAG"

if [[ "$PUSH" -eq 1 ]]; then
  BRANCH="$(git branch --show-current)"
  [[ -n "$BRANCH" ]] || {
    printf 'Cannot push from a detached HEAD.\n' >&2
    exit 1
  }

  git push origin "$BRANCH"
  git push origin "$TAG"
fi

printf 'Bumped %s to %s, committed and tagged %s.\n' "$CURRENT_VERSION" "$VERSION" "$TAG"
if [[ "$PUSH" -eq 0 ]]; then
  printf 'Push with: git push origin HEAD && git push origin %s\n' "$TAG"
fi
