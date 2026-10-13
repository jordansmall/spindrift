#!/usr/bin/env bats
# Issue #5155: a wrong `docs` verdict skips CI's heavy matrix and lands code
# unchecked, so every ambiguous case must fall to `code`.

bats_require_minimum_version 1.5.0

setup_file() {
  # One build cache for every test's ci-go-tokens build.
  export GOCACHE="${GOCACHE:-$BATS_FILE_TMPDIR/gocache}"
}

setup() {
  CI_CHANGES_SH="${CI_CHANGES_SH:-$BATS_TEST_DIRNAME/../.github/scripts/ci-changes.sh}"
}

classify() {
  run bash "$CI_CHANGES_SH"
  [ "$status" -eq 0 ]
}

@test "docs-only: edits under docs/ and root markdown classify as docs" {
  classify <<'EOF'
M	docs/reference.md
M	docs/adr/0049-role-capability-profiles-are-provider-neutral.md
M	README.md
M	CLAUDE.md
A	docs/guides/new-guide.md
EOF
  [ "$output" = docs ]
}

@test "docs-only: a release-please manifest bump alongside the changelog classifies as docs" {
  classify <<'EOF'
M	.release-please-manifest.json
M	CHANGELOG.md
EOF
  [ "$output" = docs ]
}

@test "mixed: one code path among docs classifies as code" {
  classify <<'EOF'
M	docs/reference.md
M	cmd/launcher/main.go
EOF
  [ "$output" = code ]
}

@test "prompt templates are prompt inputs, never docs" {
  classify <<'EOF'
M	templates/default/prompts/work-prompt.md
EOF
  [ "$output" = code ]
}

@test "skills are prompt inputs, never docs" {
  classify <<'EOF'
M	skills/nix-checks/SKILL.md
EOF
  [ "$output" = code ]
}

@test "nested markdown outside docs/ is not root markdown" {
  classify <<'EOF'
M	agent/notes.md
EOF
  [ "$output" = code ]
}

@test "the release-please config is not the manifest" {
  classify <<'EOF'
M	.release-please-config.json
EOF
  [ "$output" = code ]
}

@test "adding an ADR classifies as code" {
  classify <<'EOF'
A	docs/adr/0099-a-new-decision.md
EOF
  [ "$output" = code ]
}

@test "renaming an ADR classifies as code" {
  classify <<'EOF'
R097	docs/adr/0049-old-title.md	docs/adr/0049-new-title.md
EOF
  [ "$output" = code ]
}

@test "deleting an ADR classifies as code" {
  classify <<'EOF'
D	docs/adr/0049-role-capability-profiles-are-provider-neutral.md
EOF
  [ "$output" = code ]
}

@test "a rename out of docs/ into code classifies as code" {
  classify <<'EOF'
R100	docs/example.sh	agent/example.sh
EOF
  [ "$output" = code ]
}

@test "a rename within docs/ outside the ADR directory classifies as docs" {
  classify <<'EOF'
R100	docs/old-guide.md	docs/new-guide.md
EOF
  [ "$output" = docs ]
}

@test "a copy into docs/ from code classifies as code" {
  classify <<'EOF'
C075	cmd/launcher/main.go	docs/main.go.txt
EOF
  [ "$output" = code ]
}

@test "an empty diff classifies as code" {
  classify </dev/null
  [ "$output" = code ]
}

@test "a path with a docs/ prefix lookalike classifies as code" {
  classify <<'EOF'
M	docsite/index.md
EOF
  [ "$output" = code ]
}

@test "an unrecognised status line classifies as code" {
  classify <<'EOF'
garbage
EOF
  [ "$output" = code ]
}

# Issue #5158: a comment-only .go/.nix edit takes the docs lane. Each test
# commits the old content as tag `base`, the new as tag `head`, and classifies
# the real diff between them.
repo_with_base() {
  cd "$BATS_TEST_TMPDIR" && git init -q repo && cd repo
  git config user.email t@example.com
  git config user.name t
  mkdir -p "$(dirname "$1")"
  printf '%s' "$2" >"$1"
  commit base
}

commit() {
  git add -A
  git commit -qm "$1"
  git tag "$1"
}

# stderr carries why a file is code (a parse error, a cgo import); the verdict
# is stdout alone.
classify_revs() {
  run --separate-stderr bash "$CI_CHANGES_SH" base head < <(git diff --name-status -M base head)
  [ "$status" -eq 0 ]
}

classify_edit() {
  repo_with_base "$1" "$2"
  printf '%s' "$3" >"$1"
  commit head
  classify_revs
}

GO_OLD='package p

// Double returns twice n.
func Double(n int) int {
	return n * 2 // no overflow check
}
'

NIX_OLD='{ pkgs, ... }:
# Builds the probe.
{
  probe = pkgs.runCommand "probe" { } "touch $out"; # always passes
  src = ./probe.sh;
}
'

@test "comment-only: a reworded Go comment classifies as docs" {
  classify_edit cmd/p/p.go "$GO_OLD" "${GO_OLD/twice n./n doubled, unchecked.}"
  [ "$output" = docs ]
}

@test "comment-only: an added and a removed Go comment classify as docs" {
  new='package p

/* Double returns
twice n. */
func Double(n int) int {
	return n * 2
}
'
  classify_edit cmd/p/p.go "$GO_OLD" "$new"
  [ "$output" = docs ]
}

@test "comment-only: a reworded Nix comment classifies as docs" {
  classify_edit nix/checks/probe.nix "$NIX_OLD" "${NIX_OLD/Builds the probe./Builds the probe check.}"
  [ "$output" = docs ]
}

@test "comment-only: an added Nix block comment classifies as docs" {
  classify_edit nix/checks/probe.nix "$NIX_OLD" "${NIX_OLD/src =//* the script */ src =}"
  [ "$output" = docs ]
}

@test "comment-only: a reflowed Go comment classifies as docs" {
  new='package p

// Double
//   returns   twice
// n.
func Double(n int) int {
	return n * 2 // no overflow check
}
'
  classify_edit cmd/p/p.go "$GO_OLD" "$new"
  [ "$output" = docs ]
}

@test "comment-only: a reflowed Nix comment classifies as docs" {
  new='{ pkgs, ... }:
# Builds
#   the probe.
{
  probe = pkgs.runCommand "probe" { } "touch $out";
  # always passes
  src = ./probe.sh;
}
'
  classify_edit nix/checks/probe.nix "$NIX_OLD" "$new"
  [ "$output" = docs ]
}

@test "comment-only: a Go comment edit alongside a docs edit classifies as docs" {
  repo_with_base cmd/p/p.go "$GO_OLD"
  printf '%s' "${GO_OLD/twice n./n doubled.}" >cmd/p/p.go
  echo docs >README.md
  commit head
  classify_revs
  [ "$output" = docs ]
}

@test "code: a Go code change next to a comment change classifies as code" {
  new=${GO_OLD/twice n./n tripled.}
  classify_edit cmd/p/p.go "$GO_OLD" "${new/n \* 2/n * 3}"
  [ "$output" = code ]
}

@test "code: a Nix code change next to a comment change classifies as code" {
  new=${NIX_OLD/Builds the probe./Builds it.}
  classify_edit nix/checks/probe.nix "$NIX_OLD" "${new/touch/: >}"
  [ "$output" = code ]
}

@test "code: a '#' line inside a Nix indented string is code, not a comment" {
  old="{ script = ''
  # old
  true
''; }
"
  classify_edit probe.nix "$old" "${old/old/new}"
  [ "$output" = code ]
}

@test "code: a Go comment that spans a newline and moves semicolon insertion classifies as code" {
  old='package p

func f() int {
	return /* x */ 1
}
'
  new='package p

func f() int {
	return /*
	*/ 1
}
'
  classify_edit cmd/p/p.go "$old" "$new"
  [ "$output" = code ]
}

@test "code: changing a //go:build line classifies as code" {
  old='//go:build linux

package p
'
  classify_edit cmd/p/p.go "$old" "${old/linux/darwin}"
  [ "$output" = code ]
}

@test "code: a //go:build change among thousands of reworded comment lines classifies as code" {
  old=$(printf '// line %d\n' $(seq 6000))
  old="//go:build linux

package p

$old
"
  new=${old/linux/darwin}
  classify_edit cmd/p/p.go "$old" "${new//line/row}"
  [ "$output" = code ]
}

@test "code: dropping the blank line that makes a //go:build line a constraint classifies as code" {
  old='//go:build linux

package p
'
  classify_edit cmd/p/p.go "$old" '//go:build linux
package p
'
  [ "$output" = code ]
}

@test "code: dropping the blank line under a // +build line classifies as code" {
  old='// +build linux

package p
'
  classify_edit cmd/p/p.go "$old" '// +build linux
package p
'
  [ "$output" = code ]
}

@test "code: changing a //go:embed line classifies as code" {
  old='package p

import _ "embed"

//go:embed a.txt
var a string
'
  classify_edit cmd/p/p.go "$old" "${old/a.txt/b.txt}"
  [ "$output" = code ]
}

@test "code: adding a //go:generate line classifies as code" {
  classify_edit cmd/p/p.go "$GO_OLD" "${GO_OLD/package p/package p

//go:generate stringer -type=T}"
  [ "$output" = code ]
}

@test "code: adding a nolint comment classifies as code" {
  classify_edit cmd/p/p.go "$GO_OLD" "${GO_OLD/no overflow check/nolint:gosec}"
  [ "$output" = code ]
}

@test "code: a shellcheck directive in a comment classifies as code" {
  classify_edit nix/checks/probe.nix "$NIX_OLD" "${NIX_OLD/always passes/shellcheck disable=SC2154}"
  [ "$output" = code ]
}

@test "code: a cgo preamble comment edit classifies as code" {
  old='package p

// #include <stdio.h>
import "C"
'
  classify_edit cmd/p/p.go "$old" "${old/stdio/stdlib}"
  [ "$output" = code ]
}

@test "code: a comment edit in a file a parity check reads classifies as code" {
  classify_edit cmd/launcher/internal/promptassembly/gates.go "$GO_OLD" "${GO_OLD/twice n./n doubled.}"
  [ "$output" = code ]
}

@test "code: a comment edit in another listed reader file classifies as code" {
  classify_edit lib/mkHarness.nix "$NIX_OLD" "${NIX_OLD/Builds the probe./Builds it.}"
  [ "$output" = code ]
}

@test "code: editing a generated-block marker comment classifies as code" {
  old='package p

// BEGIN GENERATED SKILL-BAKED PROBES -- nix run .#regen -- DO NOT EDIT
var probes = 1

// END GENERATED SKILL-BAKED PROBES
'
  classify_edit cmd/p/p.go "$old" "${old/BEGIN GENERATED SKILL-BAKED PROBES/BEGIN GENERATED SKILL-BAKED FLAGS}"
  [ "$output" = code ]
}

@test "code: renaming a Go file with no content change classifies as code" {
  repo_with_base cmd/p/p.go "$GO_OLD"
  git mv cmd/p/p.go cmd/p/p_linux.go
  commit head
  classify_revs
  [ "$output" = code ]
}

@test "code: adding a Go file classifies as code" {
  repo_with_base README.md docs
  mkdir -p cmd/p && printf '%s' "$GO_OLD" >cmd/p/p.go
  commit head
  classify_revs
  [ "$output" = code ]
}

@test "fail closed: an unparsable Go file classifies as code" {
  old='package p

func f( {
'
  classify_edit cmd/p/p.go "$old" "// broken
$old"
  [ "$output" = code ]
}

@test "fail closed: an unparsable Nix file classifies as code" {
  old='{ a = 1;
'
  classify_edit probe.nix "$old" "# broken
$old"
  [ "$output" = code ]
}

@test "fail closed: a comment-only Go edit without base and head revs classifies as code" {
  repo_with_base cmd/p/p.go "$GO_OLD"
  printf '%s' "${GO_OLD/twice n./n doubled.}" >cmd/p/p.go
  commit head
  run bash "$CI_CHANGES_SH" < <(git diff --name-status -M base head)
  [ "$status" -eq 0 ]
  [ "$output" = code ]
}

@test "other languages stay code: a comment-only shell edit classifies as code" {
  classify_edit agent/x.sh '#!/bin/sh
# old
true
' '#!/bin/sh
# new
true
'
  [ "$output" = code ]
}
