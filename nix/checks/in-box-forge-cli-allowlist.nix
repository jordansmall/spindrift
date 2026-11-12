# Pins, as a shrink-only allowlist, every shipped prompt/skill template that
# instructs the in-Box agent to use a forge CLI (issue #6892, parent #6757):
# once the Box holds no forge credential these instructions must go, so a new
# one fails the check and a cleaned-up one must leave the list.
{ pkgs, ... }:
let
  inherit (pkgs.lib)
    assertMsg
    concatStringsSep
    escapeShellArg
    filter
    foldl'
    hasPrefix
    hasSuffix
    optional
    optionalString
    removePrefix
    toLower
    unique
    ;

  templatesDir = ../../templates/default;

  corpus =
    let
      files = builtins.concatMap (d: pkgs.lib.filesystem.listFilesRecursive (templatesDir + "/${d}")) [
        "prompts"
        "skills"
      ];
    in
    builtins.listToAttrs (
      map (f: {
        name = removePrefix "${toString templatesDir}/" (toString f);
        value = builtins.readFile f;
      }) (filter (hasSuffix ".md") files)
    );

  # A bare "." does not end a clause, so ${FORGEJO_BASE_URL:-https://codeberg.org}
  # stays whole; "..." is an ellipsis, not a stop. A list item is its own clause
  # so a negation in one bullet cannot hide an instruction in the next.
  clausesOf =
    text:
    map (builtins.replaceStrings [ "\n" ] [ " " ]) (
      filter builtins.isString (
        builtins.split "\n[[:space:]]*\n|[.!?][[:space:]]|;|\n[[:space:]]*([-*]|[0-9]+\\.) " (
          builtins.replaceStrings [ "..." ] [ "…" ] text
        )
      )
    );

  # Any gh/fj, optional global flags (and their values), then a lowercase
  # subcommand word, so a subcommand nobody listed still fails closed; "`gh`
  # would query" has no space after gh.
  cliCall = "(^|[^A-Za-z0-9_-])(gh|fj)([[:space:]]+-[^[:space:]`]*([[:space:]]+[^[:space:]`-][^[:space:]`]*)?)*[[:space:]]+[a-z][a-z-]*([^A-Za-z0-9_-]|$)";
  forgeApiHints = [
    "/api/graphql"
    "/api/v1"
    "/api/v3"
    "api.github.com"
    "FORGEJO_TOKEN"
    "GH_HOST"
    "GH_TOKEN"
    "GITHUB_API_URL"
    "GITHUB_TOKEN"
  ];

  # Split-based, not lib.hasInfix: its `.*X.*` overflows CI's std::regex Nix
  # stack on long clause text (see lib/builtins-compat.nix).
  inherit (import ../../lib/builtins-compat.nix) hasInfix;

  # What separates a marker from the previous one when it is just a list
  # continuation ("`a`, or `b`"); the piece after the last backtick drops the
  # previous call's own remainder. Known limit: nothing after the second call
  # is read, so "Do not run `a`, `b` instead" stays negated.
  continues =
    between:
    builtins.match "[[:space:],/]*((or|and|nor)[[:space:]]+)?" (
      pkgs.lib.last (builtins.split "`" between)
    ) != null;

  # A marker is prohibited only when a negation (optionally plus a verb) ends
  # the text right before it, within its own phrase: "never delivered" earlier
  # in the clause, or "do not guess: run", does not count.
  negatedBefore =
    before:
    let
      phrase = toLower (
        pkgs.lib.last (builtins.filter builtins.isString (builtins.split "—|:|,|```" before))
      );
    in
    builtins.length (
      builtins.split "(do not|don't|must not|never)( +(run|call|use|invoke))?[[:space:]]*$" phrase
    ) > 1;

  # Every marker in a clause is tested: the clause instructs unless each one is
  # negated, directly or as a continuation of the previous marker's negation.
  # "Run `gh pr view`, do not merge" and "Do not edit labels: run `gh x`" both
  # instruct.
  instructs =
    clause:
    let
      re = concatStringsSep "|" (
        [
          cliCall
          "via [$][{]FORGE_DISPLAY_NAME[}]"
        ]
        ++ optional (builtins.any (h: hasInfix h clause) forgeApiHints) "[Cc]url"
      );
      befores = filter builtins.isString (builtins.split re clause);
      negated = foldl' (
        acc: i:
        let
          before = builtins.elemAt befores i;
        in
        acc ++ [ (if i > 0 && continues before then pkgs.lib.last acc else negatedBefore before) ]
      ) [ ] (pkgs.lib.range 0 (builtins.length befores - 2));
    in
    builtins.any (n: !n) negated;

  instructing =
    files:
    builtins.attrNames (pkgs.lib.filterAttrs (_: text: builtins.any instructs (clausesOf text)) files);

  # The page says why each must go.
  allowlist =
    let
      both = name: [
        "prompts/fragments/github/${name}.md"
        "prompts/fragments/forgejo/${name}.md"
      ];
    in
    both "filer-dedup"
    ++ both "fix-ci-read"
    ++ both "open-pr-create"
    ++ both "if-blocked-pr"
    ++ both "research-verdict"
    ++ both "research-outcome-landing"
    ++ both "issue-blocked-comment"
    ++ both "filer-direct-label"
    ++ both "filer-direct-file";

  verdict =
    { files, allowlist }:
    let
      found = instructing files;
    in
    {
      unlisted = filter (f: !(builtins.elem f allowlist)) found;
      stale = filter (f: !(builtins.elem f found)) allowlist;
    };

  # The page's inventory table and the allowlist are two lists of the same
  # files; this ties them. Rows are the "| " lines under "## Inventory" up to
  # the next heading, minus the header and the `| ---` separator; the fragment
  # paths are the backticked *.md tokens in the second column.
  pageFragments =
    docText:
    let
      scan =
        acc: line:
        if hasPrefix "## " line then
          acc // { inInventory = line == "## Inventory"; }
        else if acc.inInventory && hasPrefix "| " line then
          acc // { rows = acc.rows ++ [ line ]; }
        else
          acc;
      rows =
        (foldl' scan {
          inInventory = false;
          rows = [ ];
        } (filter builtins.isString (builtins.split "\n" docText))).rows;
      dataRows = filter (r: !(hasPrefix "| ---" r)) (if rows == [ ] then [ ] else builtins.tail rows);
      fragmentCell = row: builtins.elemAt (filter builtins.isString (builtins.split "\\|" row)) 2;
      tokens = cell: map builtins.head (filter builtins.isList (builtins.split "`([^`]+\\.md)`" cell));
    in
    unique (
      map (t: if hasPrefix "prompts/" t || hasPrefix "skills/" t then t else "prompts/fragments/${t}") (
        builtins.concatMap (r: tokens (fragmentCell r)) dataRows
      )
    );

  drift =
    { docText, allowlist }:
    let
      listed = pageFragments docText;
    in
    {
      missingFromPage = filter (f: !(builtins.elem f listed)) allowlist;
      missingFromAllowlist = filter (f: !(builtins.elem f allowlist)) listed;
    };

  clean = {
    unlisted = [ ];
    stale = [ ];
  };
  noDrift = {
    missingFromPage = [ ];
    missingFromAllowlist = [ ];
  };

  instructingText = "Run `gh pr view 1` to read it.";
  fixtureOk = verdict {
    files."x.md" = instructingText;
    allowlist = [ "x.md" ];
  };
  fixtureUnlisted = verdict {
    files."x.md" = instructingText;
    allowlist = [ ];
  };
  fixtureStale = verdict {
    files."x.md" = "Nothing to run here.";
    allowlist = [ "x.md" ];
  };
  fixtureNegation = verdict {
    files = {
      "a.md" = "Do NOT run `gh pr ready`.";
      "b.md" = "You never run\n`gh pr create`, or `gh pr merge` — your\ntoken cannot.";
      "c.md" = "`gh` would query the wrong repository.";
      "d.md" = "Your token is read-only; do not call curl against /api/v1.";
      "e.md" = "Do NOT run `gh issue edit ... --add-label x`, or `gh pr merge`.";
      "f.md" = "- Do not edit labels\n- Do not run `gh issue edit`";
      "g.md" = "Do not run `gh pr create`, `gh pr merge`, or `gh issue edit`.";
      "h.md" = "do NOT `gh pr view` or `gh pr create`";
      "i.md" = "Do not run `gh -R o/r pr merge`.";
    };
    allowlist = [ ];
  };
  # A negation hides only the call it sits right before, plus the calls listed
  # after it; prose or a new instruction between them ends its reach.
  fixtureNegationScope = verdict {
    files = {
      "dash.md" = "Do not edit the issue — use `gh issue view N` instead.";
      "colon.md" = "Do not guess: run `gh issue view 1`.";
      "then.md" = "Do not run `gh pr merge`, then run `gh pr view 1`.";
      "fence.md" = "Do not edit labels:\n```\ngh issue view 1\n```\n";
      "dont.md" = "Don't forget to run `gh pr view 1`.";
      "must.md" = "You must not skip this: run `gh pr view 1`.";
    };
    allowlist = [ ];
  };
  fixtureFlags = verdict {
    files = {
      "gh.md" = "Read it with `gh -R owner/repo issue view 1`.";
      "fj.md" = "Read it with `fj -H codeberg.org issue view 1`.";
      "eq.md" = "Read it with `gh --repo=o/r issue view 1`.";
      "tab.md" = "Read it with `gh\tissue view 1`.";
    };
    allowlist = [ ];
  };
  # A negation never leaks to another clause or bullet, and the open
  # subcommand list must catch calls nobody listed.
  fixtureScoped = verdict {
    files = {
      "bullets.md" = "- Do not edit labels\n- Run `gh issue view 1` to read it";
      "mixed.md" = "Run `gh issue view N`; do not edit it.";
      "after.md" = "Run `gh pr view 1`, but do not merge it.";
      "fj.md" = "Read the CI run with `fj actions tasks`.";
      "browse.md" = "Open it with `gh browse`.";
      "variable.md" = "Check `gh variable list` first.";
      "ghes.md" =
        "Post it with curl -H \"Authorization: Bearer $GITHUB_TOKEN\" \"https://$HOST/api/v3/repos\".";
      "apiurl.md" = "Post it with curl -H \"Authorization: Bearer $T\" \"$GITHUB_API_URL/repos/o/r\".";
      "graphql.md" = "Query it with curl \"https://$GH_HOST/api/graphql\".";
    };
    allowlist = [ ];
  };
  fixtureCurl = verdict {
    files."x.md" =
      "Post it with curl -H \"Authorization: token $FORGEJO_TOKEN\" \"$BASE/api/v1/repos\".";
    allowlist = [ ];
  };
  fixtureVia = verdict {
    files."x.md" = "Pull any parent issue it references too via \${FORGE_DISPLAY_NAME}.";
    allowlist = [ ];
  };
  fixtureUrl = verdict {
    files."x.md" = "Base is \${FORGEJO_BASE_URL:-https://codeberg.org}. Run `fj issue view 1`.";
    allowlist = [ ];
  };
  fixtureNeverNotProhibition = verdict {
    files."x.md" = "Post with `gh issue comment`; it was never delivered otherwise.";
    allowlist = [ ];
  };
  fixtureNotAWord = verdict {
    files."x.md" = "Walk through pr review; high pr count.";
    allowlist = [ ];
  };

  # A table row before the Inventory heading or in a later section is not an
  # inventory row; the header row's own cells are not fragments.
  fixtureDoc = ''
    ## Earlier

    | a | `early.md` |

    ## Inventory

    | intent | fragment(s) | access |
    | ------ | ----------- | ------ |
    | read | `one.md` | both |
    | write | `github/two.md`, `forgejo/two.md` | `read-write` |
    | x | `skills/foo/SKILL.md` | both |

    ## Later

    | a | `late.md` |
  '';
  fixtureDocAllowlist = [
    "prompts/fragments/one.md"
    "prompts/fragments/github/two.md"
    "prompts/fragments/forgejo/two.md"
    "skills/foo/SKILL.md"
  ];
  fixtureDriftOk = drift {
    docText = fixtureDoc;
    allowlist = fixtureDocAllowlist;
  };
  fixtureDriftNotOnPage = drift {
    docText = fixtureDoc;
    allowlist = fixtureDocAllowlist ++ [ "prompts/fragments/three.md" ];
  };
  fixtureDriftNotAllowlisted = drift {
    docText = fixtureDoc;
    allowlist = filter (f: f != "prompts/fragments/one.md") fixtureDocAllowlist;
  };

  real = verdict {
    files = corpus;
    inherit allowlist;
  };
  realDrift = drift {
    docText = builtins.readFile ../../docs/in-box-forge-cli.md;
    inherit allowlist;
  };

  section =
    heading: hint: names:
    optionalString (names != [ ]) ''
      echo ${escapeShellArg heading}
      ${concatStringsSep "\n" (map (n: "echo ${escapeShellArg "  ${n}"}") names)}
      echo ${escapeShellArg "  -> ${hint}"}
      fail=1
    '';
in
{
  in-box-forge-cli-allowlist =
    assert assertMsg (fixtureOk == clean)
      "in-box-forge-cli-allowlist: an allowlisted instructing file must yield no findings, got: ${builtins.toJSON fixtureOk}";
    assert assertMsg (fixtureUnlisted == clean // { unlisted = [ "x.md" ]; })
      "in-box-forge-cli-allowlist: an instructing file outside the allowlist must land in unlisted, got: ${builtins.toJSON fixtureUnlisted}";
    assert assertMsg (fixtureStale == clean // { stale = [ "x.md" ]; })
      "in-box-forge-cli-allowlist: an allowlisted file that no longer instructs must land in stale, got: ${builtins.toJSON fixtureStale}";
    assert assertMsg (fixtureNegation == clean)
      "in-box-forge-cli-allowlist: negations and bare `gh` mentions must not count, got: ${builtins.toJSON fixtureNegation}";
    assert assertMsg
      (
        fixtureScoped == clean
        // {
          unlisted = [
            "after.md"
            "apiurl.md"
            "browse.md"
            "bullets.md"
            "fj.md"
            "ghes.md"
            "graphql.md"
            "mixed.md"
            "variable.md"
          ];
        }
      )
      "in-box-forge-cli-allowlist: a negation must not hide an instruction in another clause or bullet, and any gh/fj subcommand or GHES curl must be detected, got: ${builtins.toJSON fixtureScoped}";
    assert assertMsg
      (
        fixtureNegationScope == clean
        // {
          unlisted = [
            "colon.md"
            "dash.md"
            "dont.md"
            "fence.md"
            "must.md"
            "then.md"
          ];
        }
      )
      "in-box-forge-cli-allowlist: a negation must hide only the call right after it (and its list), got: ${builtins.toJSON fixtureNegationScope}";
    assert assertMsg
      (
        fixtureFlags == clean
        // {
          unlisted = [
            "eq.md"
            "fj.md"
            "gh.md"
            "tab.md"
          ];
        }
      )
      "in-box-forge-cli-allowlist: global flags before the subcommand must not hide a call, got: ${builtins.toJSON fixtureFlags}";
    assert assertMsg (fixtureCurl == clean // { unlisted = [ "x.md" ]; })
      "in-box-forge-cli-allowlist: curl against a forge API must be detected, got: ${builtins.toJSON fixtureCurl}";
    assert assertMsg (fixtureVia == clean // { unlisted = [ "x.md" ]; })
      "in-box-forge-cli-allowlist: a generic 'via \${FORGE_DISPLAY_NAME}' read must be detected, got: ${builtins.toJSON fixtureVia}";
    assert assertMsg (fixtureUrl == clean // { unlisted = [ "x.md" ]; })
      "in-box-forge-cli-allowlist: a URL's dot must not split a sentence, got: ${builtins.toJSON fixtureUrl}";
    assert assertMsg (fixtureNotAWord == clean)
      "in-box-forge-cli-allowlist: 'gh'/'pr' inside a longer word must not match, got: ${builtins.toJSON fixtureNotAWord}";
    assert assertMsg (fixtureNeverNotProhibition == clean // { unlisted = [ "x.md" ]; })
      "in-box-forge-cli-allowlist: only \"never run\" negates; \"never delivered\" must not hide an instruction, got: ${builtins.toJSON fixtureNeverNotProhibition}";
    assert assertMsg (fixtureDriftOk == noDrift)
      "in-box-forge-cli-allowlist: a page whose inventory matches the allowlist must report no drift, got: ${builtins.toJSON fixtureDriftOk}";
    assert assertMsg
      (fixtureDriftNotOnPage == noDrift // { missingFromPage = [ "prompts/fragments/three.md" ]; })
      "in-box-forge-cli-allowlist: an allowlist entry with no inventory row must land in missingFromPage, got: ${builtins.toJSON fixtureDriftNotOnPage}";
    assert assertMsg
      (
        fixtureDriftNotAllowlisted == noDrift // { missingFromAllowlist = [ "prompts/fragments/one.md" ]; }
      )
      "in-box-forge-cli-allowlist: an inventory row with no allowlist entry must land in missingFromAllowlist, got: ${builtins.toJSON fixtureDriftNotAllowlisted}";
    assert assertMsg (corpus != { } && allowlist != [ ])
      "in-box-forge-cli-allowlist: expected a non-empty templates/default corpus and allowlist -- check is vacuous";
    pkgs.runCommand "in-box-forge-cli-allowlist" { } ''
      fail=0
      ${section "templates that instruct a forge CLI and are not allowlisted:"
        "stop instructing the in-Box agent to use a forge CLI (see docs/in-box-forge-cli.md); do not extend the allowlist in nix/checks/in-box-forge-cli-allowlist.nix"
        real.unlisted
      }
      ${section "allowlisted templates that no longer instruct a forge CLI:"
        "remove the allowlist entry from nix/checks/in-box-forge-cli-allowlist.nix"
        real.stale
      }
      ${section "allowlisted templates with no row in docs/in-box-forge-cli.md:"
        "add the row to the page's Inventory table, or remove the allowlist entry"
        realDrift.missingFromPage
      }
      ${section "docs/in-box-forge-cli.md inventory rows with no allowlist entry:"
        "remove the row from the page, or add the allowlist entry in nix/checks/in-box-forge-cli-allowlist.nix"
        realDrift.missingFromAllowlist
      }
      [ "$fail" = 0 ] || exit 1
      touch $out
    '';
}
