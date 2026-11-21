# Guards the doctor report-routing refactor (issue #3776): every line the
# `spindrift doctor` report stream prints goes through Reporter
# (cmd/launcher/internal/doctor/report.go), never a raw write call scattered
# elsewhere in the doctor call graph.
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    concatMap
    concatMapStrings
    dirOf
    escapeShellArg
    imap0
    listToAttrs
    replaceStrings
    ;

  internalDoctorSrc = builtins.readFile ../../cmd/launcher/internal/doctor/doctor.go;
  internalReportSrc = builtins.readFile ../../cmd/launcher/internal/doctor/report.go;
  launchgatesSrc = builtins.readFile ../../cmd/launcher/launchgates.go;
  doctorMainSrc = builtins.readFile ../../cmd/launcher/doctor.go;

  # scan_files greps each .go file a rule hands it for write call sites and
  # rejects every writer but the rule's permitted identifiers (empty = none).
  # They are pinned literally (w, checkW, stderr), not resolved by type, so
  # renaming one is a loud false-fail, the right failure mode for a guard this
  # cheap.
  # Recognises fmt.Fprint*, io.WriteString(writer, …) and
  # writer.Write([]byte(…)), with the writer on the call's open-paren line;
  # any other spelling, or a writer wrapped to the next line, escapes it.
  # Rules:
  #   1. internal/doctor/report.go is the only non-test .go file under
  #      internal/doctor allowed to write. The exemption is that path, not the
  #      basename, so a future internal/doctor/sub/report.go is scanned.
  #   2. launchgates.go may write only to w (launchGate.Check's writer) or
  #      checkW (walkGateRegistry's name for it): a gate Check writing its own
  #      operator-facing output is what AC7 protects, though none does today.
  #   3. every write left in cmd/launcher/doctor.go must target stderr: stdout
  #      is the report stream, reached only via doctor.NewReporter.
  # Rules 2 and 3 name only those files: most of cmd/launcher legitimately
  # writes to stdout, and only these two sit in the doctor report call graph.
  scanScript = pkgs.writeShellScript "doctor-report-routing-scan" ''
    set -euo pipefail

    dir="$1"
    status=0

    # The writer sits right after the opening paren for fmt.Fprint*/
    # io.WriteString, and right before ".Write(" for the byte-slice idiom.
    writer_of() {
      local m="$1"
      if [[ "$m" == *.Write\(* ]]; then
        printf '%s' "''${m%%.Write(*}"
      else
        printf '%s' "''${m##*(}"
      fi
    }

    # allowed is a space-separated set of permitted writer identifiers.
    scan_files() {
      local allowed="$1" message="$2" perm_desc
      shift 2
      local file matches m lineno writer candidate permitted
      if [ -n "$allowed" ]; then
        perm_desc="only ''${allowed// /, } may write here directly"
      else
        perm_desc="no writer may write here directly"
      fi
      for file in "$@"; do
        # -o matches per call site, not per line, so a line mixing a permitted
        # and a forbidden writer still flags the forbidden one.
        matches=$(grep -noE 'fmt\.Fprint(f|ln)?\([A-Za-z_][A-Za-z0-9_.]*|io\.WriteString\([A-Za-z_][A-Za-z0-9_.]*|[A-Za-z_][A-Za-z0-9_.]*\.Write\(\[\]byte' "$file" || true)
        [ -z "$matches" ] && continue
        while IFS= read -r m; do
          lineno="''${m%%:*}"
          writer=$(writer_of "''${m#*:}")
          permitted=""
          for candidate in $allowed; do
            if [ "$writer" = "$candidate" ]; then
              permitted=yes
              break
            fi
          done
          if [ -n "$permitted" ]; then
            continue
          fi
          echo "doctor-report-routing: $file:$lineno writes via '$writer' ($perm_desc) — $message" >&2
          status=1
        done <<< "$matches"
      done
    }

    if [ -d "$dir/internal/doctor" ]; then
      files=()
      while IFS= read -r -d "" file; do
        files+=("$file")
      done < <(find "$dir/internal/doctor" -name '*.go' ! -name '*_test.go' ! -path "$dir/internal/doctor/report.go" -print0)
      if [ "''${#files[@]}" -eq 0 ]; then
        echo "doctor-report-routing: no non-report.go .go file found under $dir/internal/doctor — vacuous pass"
      else
        scan_files "" "route this line through Reporter in cmd/launcher/internal/doctor/report.go instead" "''${files[@]}"
      fi
    else
      echo "doctor-report-routing: $dir/internal/doctor not found — vacuous pass"
    fi

    if [ -f "$dir/launchgates.go" ]; then
      scan_files "w checkW" "its report rows must go through Reporter in cmd/launcher/internal/doctor/report.go, or write to the gate Check's own writer directly if it is that Check's own operator-facing output (AC7)" "$dir/launchgates.go"
    else
      echo "doctor-report-routing: $dir/launchgates.go not found — vacuous pass"
    fi

    if [ -f "$dir/doctor.go" ]; then
      scan_files "stderr os.Stderr" "stdout is the report stream and must go through Reporter in cmd/launcher/internal/doctor/report.go via doctor.NewReporter(stdout, opts.verbose)" "$dir/doctor.go"
    else
      echo "doctor-report-routing: $dir/doctor.go not found — vacuous pass"
    fi

    exit "$status"
  '';
in
{
  doctor-report-routing = pkgs.runCommand "doctor-report-routing" { } ''
    ${scanScript} ${../../cmd/launcher}
    touch $out
  '';

  # Exercises scanScript against doctored copies of the real tree: a reject
  # fixture per rule catches a guard that can only pass, and accept fixtures
  # for each rule's permitted writes catch one that can only fail (e.g. rule 2
  # permitting a writer name no gate Check binds). Each doctored source is a
  # replaceStrings anchored on real text, so a rewritten anchor surfaces in
  # checkDiffers as a doctored == original no-op. checkDiffers only proves one
  # match fired: a second unintended match elsewhere goes unnoticed.
  doctor-report-routing-regression =
    let
      leakFn = "\n\nfunc doctorReportRoutingLeak() {\n\tfmt.Fprintln(nil, \"leak\")\n}\n";
      # All three permitted idioms, written to the gate Check signature's own
      # writer name (launchGate.Check in launchgates.go). Fixture sources are
      # only ever grepped, never compiled, so an unused parameter or import
      # does not matter.
      permittedFn = "\n\nfunc doctorReportRoutingPermitted(w io.Writer) {\n\tfmt.Fprintln(w, \"note\")\n\tio.WriteString(w, \"note\")\n\tw.Write([]byte(\"note\"))\n}\n";

      fixtures = [
        {
          name = "a raw fmt.Fprintln added to internal/doctor/doctor.go, outside report.go";
          scratchDir = "reject-rule1";
          expectMessage = "writes via 'nil' (no writer may write here directly)";
          files = [
            {
              relPath = "internal/doctor/doctor.go";
              original = internalDoctorSrc;
              anchor = "the package doctor clause";
              contents =
                replaceStrings
                  [ "package doctor\n" ]
                  [
                    "package doctor${leakFn}"
                  ]
                  internalDoctorSrc;
            }
          ];
        }
        {
          # report.go is exempt by path, not by basename, so a subpackage that
          # reuses the name is still scanned.
          name = "a raw fmt.Fprintln in a future internal/doctor/sub/report.go";
          scratchDir = "reject-rule1-sub";
          expectMessage = "sub/report.go:4 writes via 'nil' (no writer may write here directly)";
          files = [
            {
              # Synthetic: no real subpackage exists to anchor a fixture on.
              relPath = "internal/doctor/sub/report.go";
              contents = "package sub${leakFn}";
            }
          ];
        }
        {
          name = "a raw fmt.Fprintln added to launchgates.go";
          scratchDir = "reject-rule2";
          expectMessage = "writes via 'nil' (only w, checkW may write here directly)";
          files = [
            {
              relPath = "launchgates.go";
              original = launchgatesSrc;
              anchor = "the package main clause";
              contents =
                replaceStrings
                  [ "package main\n" ]
                  [
                    "package main${leakFn}"
                  ]
                  launchgatesSrc;
            }
          ];
        }
        {
          name = "doctor.go's fmt.Fprintf(stderr, ...) configErr line retargeted at stdout";
          scratchDir = "reject-rule3";
          expectMessage = "writes via 'stdout' (only stderr, os.Stderr may write here directly)";
          files = [
            {
              relPath = "doctor.go";
              original = doctorMainSrc;
              anchor = "the configErr stderr line";
              contents =
                replaceStrings
                  [ ''fmt.Fprintf(stderr, "%s\n", configErr)'' ]
                  [
                    ''fmt.Fprintf(stdout, "%s\n", configErr)''
                  ]
                  doctorMainSrc;
            }
          ];
        }
        {
          # The rule-1 positive control: report.go's own raw writes are the
          # point of the exemption, and they only prove it while a sibling
          # file keeps the scan from taking the vacuous-pass branch.
          name = "internal/doctor/report.go's own raw writes, alongside a clean sibling";
          scratchDir = "accept-rule1";
          expect = "accept";
          files = [
            {
              relPath = "internal/doctor/report.go";
              contents = internalReportSrc;
            }
            {
              relPath = "internal/doctor/doctor.go";
              contents = internalDoctorSrc;
            }
          ];
        }
        {
          # The rule-2 positive control (AC7): a gate Check writing its own
          # operator-facing output must pass, in every recognised idiom.
          name = "a gate Check in launchgates.go writing its own operator-facing output to w";
          scratchDir = "accept-rule2";
          expect = "accept";
          files = [
            {
              relPath = "launchgates.go";
              original = launchgatesSrc;
              anchor = "the package main clause";
              contents =
                replaceStrings
                  [ "package main\n" ]
                  [
                    "package main${permittedFn}"
                  ]
                  launchgatesSrc;
            }
          ];
        }
        {
          name = "doctor.go's configErr line spelled against os.Stderr rather than the stderr param";
          scratchDir = "accept-rule3";
          expect = "accept";
          files = [
            {
              relPath = "doctor.go";
              original = doctorMainSrc;
              anchor = "the configErr stderr line";
              contents =
                replaceStrings
                  [ ''fmt.Fprintf(stderr, "%s\n", configErr)'' ]
                  [
                    ''fmt.Fprintf(os.Stderr, "%s\n", configErr)''
                  ]
                  doctorMainSrc;
            }
          ];
        }
      ];

      # Each file's source reaches the builder through an env var rather than
      # the script text, so shell metacharacters in Go source cannot rewrite
      # the builder. The name derives from the fixture, dashes mapped to
      # underscores: Nix will name an env var the shell cannot expand.
      fixtureFiles =
        f:
        imap0 (
          i: file:
          file
          // {
            envVar = "SRC_${replaceStrings [ "-" ] [ "_" ] f.scratchDir}_${toString i}";
          }
        ) f.files;
      allFiles = concatMap fixtureFiles fixtures;

      # Guards the fixtures themselves: if the anchored line in the real source
      # is rewritten so a replaceStrings match stops firing, the doctored source
      # would equal the original and the scan would run against effectively
      # undoctored input, an unnoticed vacuous check.
      checkDiffers =
        file:
        assert assertMsg (!(file ? original) || file.contents != file.original)
          "doctor-report-routing-regression: a replaceStrings fixture found no match in its source file — update its anchor to match ${
            file.anchor or "the real source"
          }";
        true;

      # ''$ escapes the shell's literal `$`, then ${file.envVar} interpolates;
      # `$${file.envVar}` would write that literal text instead. The -s test
      # catches every other way a fixture lands empty: an empty fixture would
      # pass both a reject and an accept assertion for the wrong reason.
      writeFixture =
        f:
        concatMapStrings (file: ''
          mkdir -p ${f.scratchDir}/${dirOf file.relPath}
          printf '%s' "''$${file.envVar}" > ${f.scratchDir}/${file.relPath}
          if [ ! -s ${f.scratchDir}/${file.relPath} ]; then
            echo "doctor-report-routing-regression: fixture ${f.scratchDir}/${file.relPath} is empty — ${file.envVar} did not reach the builder" >&2
            exit 1
          fi
        '') (fixtureFiles f);

      # A reject fixture must fail for its own rule, not for an unrelated
      # crash (a missing tool, a shifted argument), so the expected rule's
      # message is asserted too, not just the non-zero exit.
      runFixture =
        f:
        if (f.expect or "reject") == "accept" then
          ''
            if ! ${scanScript} ${f.scratchDir} >${f.scratchDir}.log 2>&1; then
              echo "doctor-report-routing-regression: expected the scan to accept ${f.name}, but it exited non-zero:" >&2
              cat ${f.scratchDir}.log >&2
              exit 1
            fi
          ''
        else
          ''
            if ${scanScript} ${f.scratchDir} >${f.scratchDir}.log 2>&1; then
              echo "doctor-report-routing-regression: expected the scan to reject ${f.name}, but it exited 0" >&2
              exit 1
            fi
            if ! grep -qF ${escapeShellArg f.expectMessage} ${f.scratchDir}.log; then
              echo "doctor-report-routing-regression: the scan rejected ${f.name}, but not with the expected message (want substring: ${f.expectMessage}); it printed:" >&2
              cat ${f.scratchDir}.log >&2
              exit 1
            fi
          '';
    in
    assert builtins.all checkDiffers allFiles;
    pkgs.runCommand "doctor-report-routing-regression"
      (listToAttrs (
        map (file: {
          name = file.envVar;
          value = file.contents;
        }) allFiles
      ))
      ''
        ${concatMapStrings writeFixture fixtures}
        ${concatMapStrings runFixture fixtures}
        touch $out
      '';
}
