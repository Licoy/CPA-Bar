#!/usr/bin/env bash
set -euo pipefail

cliff="${GIT_CLIFF:-git-cliff}"
range_script="$(pwd)/scripts/release-notes-range.sh"
repo="$(mktemp -d)"
trap 'rm -rf "$repo"' EXIT
cp cliff.toml "$repo/"
cd "$repo"
git init -q
git config user.name test
git config user.email test@example.com

commit() {
  git commit --allow-empty -qm "$1"
}

notes() {
  local range
  range="$("$range_script" "$1")"
  "$cliff" --config cliff.toml --strip header --output notes.md "$range"
}

reject() {
  if grep -Fq "$1" notes.md; then
    echo "unexpected release note: $1" >&2
    exit 1
  fi
}

commit 'Initial commit'
commit 'feat: first release feature'
git tag v1.0.0
commit 'feat: add release notes'
commit 'keep this unconventional commit'
commit 'fix: keep $(touch injected) and `touch injected-too` literal'
commit 'chore(release): bump version to 1.1.0'
git tag v1.1.0
notes v1.1.0
grep -Fq 'add release notes' notes.md
grep -Fq 'keep this unconventional commit' notes.md
grep -Fq '$(touch injected)' notes.md
grep -Fq 'github.com/Licoy/CPA-Bar/commit/' notes.md
reject 'bump version'
if [ -e injected ] || [ -e injected-too ]; then
  echo 'release note text was executed as shell code' >&2
  exit 1
fi
cp notes.md v1.1.0-notes.md

commit 'feat: future release only'
git tag v1.2.0
notes v1.2.0
grep -Fq 'future release only' notes.md
reject 'add release notes'
reject 'keep this unconventional commit'
notes v1.1.0
cmp -s notes.md v1.1.0-notes.md || { echo 'old tag notes changed after a later tag' >&2; exit 1; }
notes v1.0.0
grep -Fq 'Initial commit' notes.md
grep -Fq 'first release feature' notes.md
reject 'add release notes'
reject 'future release only'

git switch -q -c side v1.2.0
commit 'fix: side release commit'
git tag v1.3.0
git switch -q -c merge-release v1.2.0
commit 'feat: main release commit'
git merge -q --no-ff side -m 'merge release histories'
git tag v1.4.0
notes v1.4.0
grep -Fq 'main release commit' notes.md
reject 'side release commit'
