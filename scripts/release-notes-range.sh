#!/usr/bin/env bash
set -euo pipefail

tag="${1:?usage: release-notes-range.sh <tag>}"
current="$(git rev-parse "$tag^{commit}")"
reachable_tags="$(git tag --merged "$current" --list 'v[0-9]*')"
exclude=()
has_previous=false

while IFS= read -r reachable; do
  [ -n "$reachable" ] || continue
  if [ "$(git rev-parse "$reachable^{commit}")" = "$current" ]; then
    exclude+=(--exclude "$reachable")
  else
    has_previous=true
  fi
done <<< "$reachable_tags"

if [ "$has_previous" = true ]; then
  previous_tag="$(git describe --tags --match 'v[0-9]*' "${exclude[@]}" --abbrev=0 "$current")"
  previous="$(git rev-parse "$previous_tag^{commit}")"
  printf '%s..%s\n' "$previous" "$current"
else
  printf '%s\n' "$current"
fi
