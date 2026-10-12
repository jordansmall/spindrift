#!/usr/bin/env bats
# Issue #5155: a wrong `docs` verdict skips CI's heavy matrix and lands code
# unchecked, so every ambiguous case must fall to `code`.

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
