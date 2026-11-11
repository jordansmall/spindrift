# Issue #6792: wazero is the Connector sandbox (ADR 0073), so a floating
# version would let `go get -u` or a pseudo-version swap the sandbox silently.
# Fails unless cmd/launcher/go.mod requires github.com/tetratelabs/wazero
# directly, exactly once, at a plain release `vX.Y.Z`, with no `replace` or
# `exclude` naming it. go.mod is parsed by `go mod edit -json`, not matched as
# text, so quoted and escaped module paths cannot slip past the guard.
{ pkgs, ... }:
let
  mod = "github.com/tetratelabs/wazero";

  # One problem per line; empty output means the pin holds.
  jqFilter = ''
    [.Require // [] | .[] | select(.Path == $mod)] as $req
    | (
        if ($req | length) == 0 then ["no \($mod) requirement"]
        elif ($req | length) > 1 then ["\($mod) listed \($req | length) times"]
        else
          (if $req[0].Indirect then ["\($mod) is only an indirect requirement"] else [] end)
          + (if ($req[0].Version | test("^v(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)$"))
             then [] else ["\($mod) is at \($req[0].Version), not an exact release vX.Y.Z"] end)
        end
      )
      + [.Replace // [] | .[] | select(.Old.Path == $mod) | "\($mod) is replaced by \(.New.Path)"]
      + [.Exclude // [] | .[] | select(.Path == $mod) | "\($mod) \(.Version) is excluded"]
    | .[]
  '';

  # A writeShellApplication so shellcheck vets the guard; the check runs it.
  guard = pkgs.writeShellApplication {
    name = "wazero-pin-guard";
    runtimeInputs = [
      pkgs.go
      pkgs.jq
    ];
    text = ''
      goMod=$1 mod=${pkgs.lib.escapeShellArg mod}
      export HOME="$TMPDIR/home" GOCACHE="$TMPDIR/gocache" GOPATH="$TMPDIR/gopath"
      export GOTOOLCHAIN=local GOFLAGS="" GOPROXY=off
      work="$TMPDIR/fixture"
      mkdir -p "$HOME" "$work"

      # Problem lines for one go.mod; a go.mod `go mod edit` cannot parse is
      # itself a problem, so it cannot pass by being unreadable.
      problems() {
        local json
        if ! json=$(go mod edit -json "$1" 2>&1); then
          echo "go mod edit failed: $json"
          return
        fi
        jq -r --arg mod "$mod" -f ${pkgs.writeText "wazero-pin.jq" jqFilter} <<<"$json"
      }

      gomod() {
        { printf 'module example.com/m\n\ngo 1.21\n\n'; cat; } >"$work/go.mod"
      }

      selftest_failed=0
      # expect NAME WANT: compare problems for the fixture on stdin.
      expect() {
        local name=$1 want=$2 got
        gomod
        got=$(problems "$work/go.mod")
        if [ "$got" != "$want" ]; then
          printf 'self-test %s failed:\n  got:  %s\n  want: %s\n' "$name" "$got" "$want"
          selftest_failed=1
        fi
      }

      expect release "" <<'EOF'
      require (
      	${mod} v1.11.0
      )
      EOF
      expect singleLineRelease "" <<'EOF'
      require ${mod} v1.11.0
      EOF
      expect quotedRequireRelease "" <<'EOF'
      require "${mod}" v1.11.0
      EOF
      expect pseudoVersionFails "${mod} is at v0.0.0-20240101000000-abcdef123456, not an exact release vX.Y.Z" <<'EOF'
      require ${mod} v0.0.0-20240101000000-abcdef123456
      EOF
      expect preReleaseFails "${mod} is at v1.11.0-rc.1, not an exact release vX.Y.Z" <<'EOF'
      require ${mod} v1.11.0-rc.1
      EOF
      # go canonicalises the shorthand `v1.11` to `v1.11.0`, so it is a pin.
      expect truncatedCanonicalised "" <<'EOF'
      require ${mod} v1.11
      EOF
      expect incompatibleFails "${mod} is at v2.0.0+incompatible, not an exact release vX.Y.Z" <<'EOF'
      require ${mod} v2.0.0+incompatible
      EOF
      expect longerPathIgnored "no ${mod} requirement" <<'EOF'
      require ${mod}-extra v1.0.0
      EOF
      expect missingFails "no ${mod} requirement" <<'EOF'
      require example.com/x v1.0.0
      EOF
      expect indirectFails "${mod} is only an indirect requirement" <<'EOF'
      require ${mod} v1.11.0 // indirect
      EOF
      expect duplicateFails "${mod} listed 2 times" <<'EOF'
      require (
      	${mod} v1.11.0
      	${mod} v1.10.0
      )
      EOF
      expect replaceFails "${mod} is replaced by ../wazero" <<'EOF'
      require ${mod} v1.11.0
      replace ${mod} => ../wazero
      EOF
      expect quotedReplaceFails "${mod} is replaced by ./w" <<'EOF'
      require ${mod} v1.11.0
      replace "${mod}" => ./w
      EOF
      expect escapedReplaceFails "${mod} is replaced by ./w" <<'EOF'
      require ${mod} v1.11.0
      replace "github.com/tetratelabs/wazer\x6f" => ./w
      EOF
      expect replaceBlockEntryFails "${mod} is replaced by example.com/fork" <<'EOF'
      require ${mod} v1.11.0
      replace (
      	${mod} => example.com/fork v1.0.0
      )
      EOF
      expect excludeFails "${mod} v1.10.0 is excluded" <<'EOF'
      require ${mod} v1.11.0
      exclude ${mod} v1.10.0
      EOF
      expect quotedExcludeFails "${mod} v1.10.0 is excluded" <<'EOF'
      require ${mod} v1.11.0
      exclude "${mod}" v1.10.0
      EOF
      expect excludeBlockEntryFails "${mod} v1.10.0 is excluded" <<'EOF'
      require ${mod} v1.11.0
      exclude (
      	${mod} v1.10.0
      )
      EOF
      if [ "$selftest_failed" -ne 0 ]; then
        echo "wazero-pin-guard self-test failed" >&2
        exit 1
      fi

      found=$(problems "$goMod")
      if [ -n "$found" ]; then
        printf 'cmd/launcher/go.mod does not pin wazero (issue #6792):\n%s\n' "$found" >&2
        exit 1
      fi
    '';
  };
in
{
  wazero-pin-guard = pkgs.runCommand "wazero-pin-guard" { goMod = ../../cmd/launcher/go.mod; } ''
    ${guard}/bin/wazero-pin-guard "$goMod"
    touch "$out"
  '';
}
