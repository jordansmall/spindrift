# The one shared marker-splice + drift-comparison implementation for every
# "committed doc/file has a BEGIN/END-marker-delimited span that must match
# some generated Nix source of truth" check in this repo (issue #2949).
# Before this file existed, nix/checks/schema-drift.nix's assertMarkedBlockOk,
# nix/checks/baked-skills.nix's between/assertSpanOk/assertGoSpanGofmtOk
# (now assertSplicedSpanOk), and nix/regen.nix's own bash-side splicer were
# three independently hand-mirrored copies of the same marker-splitting logic
# (baked-skills.nix's own header comment said as much) -- a classic setup for
# the copies to quietly diverge. nix/checks/schema-drift.nix,
# nix/checks/baked-skills.nix, and nix/regen.nix all import this file instead
# of defining their own copy, so there is exactly one place this logic can be
# edited.
#
# Marker convention (matches lib/documented-facts.nix /
# lib/documented-fact-shape.nix): `beginMarker` carries its own trailing
# "\n"; `endMarker` carries none.
{ pkgs }:
rec {
  # Isolates the text strictly between a literal begin/end marker line pair
  # inside `docSrc`, else throws naming `docPath`/`blockName`. escapeRegex
  # guards `builtins.split`, which reads its pattern argument as a POSIX
  # extended regex, not a literal string -- a marker that ever picks up a
  # metacharacter (".", "(", "*", ...) would otherwise silently mis-split
  # instead of splitting on itself literally (issue #2948).
  splitMarkedBlock =
    {
      blockName,
      beginMarker,
      endMarker,
      docSrc,
      docPath,
    }:
    let
      inherit (pkgs.lib) escapeRegex;
      afterBegin =
        let
          parts = builtins.split (escapeRegex beginMarker) docSrc;
        in
        if builtins.length parts >= 3 then
          builtins.elemAt parts 2
        else
          throw "${docPath}: BEGIN GENERATED ${blockName} marker not found";
      committed =
        let
          parts = builtins.split (escapeRegex endMarker) afterBegin;
        in
        if builtins.length parts >= 3 then
          builtins.elemAt parts 0
        else
          throw "${docPath}: END GENERATED ${blockName} marker not found";
    in
    committed;

  # Asserts the marker-delimited sub-block isolated by splitMarkedBlock
  # matches `generated`, else throws a message naming the sub-block
  # (blockName) and the schema file it drifted from (sourceDesc). Returns
  # `docSrc` unchanged on success.
  assertMarkedBlockOk =
    {
      blockName,
      sourceDesc,
      beginMarker,
      endMarker,
      docSrc,
      generated,
      docPath,
    }:
    let
      inherit (pkgs.lib) assertMsg;
      committed = splitMarkedBlock {
        inherit
          blockName
          beginMarker
          endMarker
          docSrc
          docPath
          ;
      };
    in
    assert assertMsg (committed == generated) ''
      ${docPath} generated ${blockName} sub-block is out of sync with ${sourceDesc} — regenerate it with `nix run .#regen`
        got:  ${committed}
        want: ${generated}'';
    docSrc;

  # The awk technique that replaces the text strictly between (and
  # preserving) a literal begin/end marker line pair with the contents of a
  # raw file -- a single bash function definition interpolated into every
  # runCommand script that needs it, so every splice call site shares one
  # implementation instead of forking a second copy.
  spliceShellFn = ''
    splice() {
      local committed="$1" beginMarker="$2" endMarker="$3" rawfile="$4" outfile="$5"
      awk -v begin="$beginMarker" -v end="$endMarker" -v rawfile="$rawfile" '
        BEGIN { while ((getline line < rawfile) > 0) content = content line "\n" }
        $0 == begin { print; printf "%s", content; skip=1; next }
        $0 == end { skip=0 }
        skip { next }
        { print }
      ' "$committed" > "$outfile"
    }
  '';

  # Some generated spans live inside a block that a formatter reflows across
  # the whole surrounding construct (e.g. `gofmt -w` column-aligning a Go
  # struct/const block), so a raw string diff against `generated` isn't
  # enough -- reconstruct the real file with the span replaced by the raw
  # (unformatted) generated content, optionally run `gofmt -w` over the whole
  # reconstruction, then diff against the committed file. Callers needing no
  # such reformatting leave `gofmt` at its default `false` and this
  # degenerates to a plain splice-then-diff. Forces the same eval-time
  # marker-presence check splitMarkedBlock gives assert*Ok callers before
  # ever constructing the runCommand derivation below, so a missing marker
  # fails loudly at eval time instead of the awk splice silently falling
  # through and the diff below reporting no drift even though the generated
  # content never got inserted.
  assertSplicedSpanOk =
    {
      name,
      file,
      blockName,
      sourceDesc,
      beginMarker,
      endMarker,
      generated,
      # When true, runs `gofmt -w` over the reconstructed file before
      # diffing (for spans living inside a gofmt-reflowed construct) and
      # folds `pkgs.go` into nativeBuildInputs automatically. Every real
      # call site needing reformatting wants exactly this one command, so
      # there is no reason to make callers spell "gofmt -w reconstructed"
      # (an internal detail -- "reconstructed" is this function's own
      # outfile, not something callers should need to know) or pkgs.go
      # themselves.
      gofmt ? false,
      nativeBuildInputs ? [ ],
      # When true, inverts the final diff assertion: the build succeeds only
      # when the reconstructed span does NOT match the committed file (i.e. a
      # synthetic drift was correctly detected) and fails when it DOES match
      # (i.e. the rejection path silently failed to catch drift). Lets a
      # regression guard (documented-fact-guard) drive this function's own
      # real diff-rejection path against a synthetic drift, instead of only
      # ever exercising its happy path (issue #2949 review finding).
      expectMismatch ? false,
    }:
    let
      inherit (pkgs.lib) removeSuffix;
      raw = pkgs.writeText "${name}.raw" generated;
      beginLine = removeSuffix "\n" beginMarker;
      markersPresent = splitMarkedBlock {
        inherit blockName beginMarker endMarker;
        docSrc = builtins.readFile file;
        docPath = toString file;
      };
      postProcessStep = if gofmt then "gofmt -w reconstructed" else "";
      effectiveNativeBuildInputs = nativeBuildInputs ++ (if gofmt then [ pkgs.go ] else [ ]);
      diffStep =
        if expectMismatch then
          ''
            if diff reconstructed "$committed" >/dev/null; then
              echo "${name}: expected the reconstructed span to differ from the committed file (synthetic drift), but it matched -- assertSplicedSpanOk's rejection path is not actually catching drift" >&2
              exit 1
            else
              touch $out
            fi
          ''
        else
          ''
            diff reconstructed "$committed" \
              || { echo "${toString file} generated ${blockName} span is out of sync with ${sourceDesc} -- regenerate it with \`nix run .#regen\`" >&2; exit 1; }
            touch $out
          '';
    in
    builtins.seq markersPresent (
      pkgs.runCommand name
        {
          nativeBuildInputs = effectiveNativeBuildInputs;
          committed = file;
          inherit raw;
          beginMarker = beginLine;
          inherit endMarker;
        }
        ''
          ${spliceShellFn}
          splice "$committed" "$beginMarker" "$endMarker" "$raw" reconstructed
          ${postProcessStep}
          ${diffStep}
        ''
    );
}
