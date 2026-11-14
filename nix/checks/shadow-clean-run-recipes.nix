# Issue #7041: the shadow-mode "consecutive clean runs" recipes in
# docs/reference.md are shell a human pastes, so nothing ran them. This
# extracts both fenced blocks from the doc as written and runs each over small
# fixture log sets, so doc and check cannot drift.
#
# Cases pin: a run still in flight is skipped, not counted dirty (an unended
# Pass whose Dispatch's primary `issue-<n>.log` has no `dispatch_settled` for
# claude; a live file-name pid for github); a finished run with a missing
# summary is dirty, a killed `-fix-N` satellite included, since only the
# primary log is ever settled; and a stamp-less log never sorts ahead of
# stamped ones, which the claude cases run under en_US.UTF-8 to catch (the
# locale ignores the leading tab, so a plain `sort -r` puts the empty stamp
# first; a malformed stamp sorts like a missing one but keeps its verdict).
# Stamps carry mixed UTC offsets, so they order by instant, not text. Each
# recipe's `finished=` toggle is also run set to 1, for logs fetched from CI,
# which have all ended: then an unended Pass or missing summary counts dirty.
{ pkgs, ... }:
{
  shadow-clean-run-recipes =
    pkgs.runCommand "shadow-clean-run-recipes"
      {
        nativeBuildInputs = [
          pkgs.gawk
          pkgs.jq
        ];
        LOCALE_ARCHIVE = "${pkgs.glibcLocales}/lib/locale/locale-archive";
        docs = ../../docs/reference.md;
      }
      ''
        # The first ```sh block whose body holds the marker; empty if none.
        extract() {
          awk -v m="$1" '
            /^```sh$/ { inb = 1; body = ""; next }
            /^```$/ { if (inb && index(body, m)) { printf "%s", body; exit } inb = 0; next }
            inb { body = body $0 "\n" }' "$docs"
        }
        extract driver=claude > claude.sh
        extract backend=github > github.sh
        fail=0
        for r in claude.sh github.sh; do
          if ! grep -q '^logs=\.spindrift/logs$' "$r"; then
            echo "FAIL: no recipe extracted into $r from docs/reference.md" >&2
            exit 1
          fi
          sed 's/^finished=$/finished=1/' "$r" > "''${r%.sh}-finished.sh"
          if ! grep -q '^finished=1$' "''${r%.sh}-finished.sh"; then
            echo "FAIL: $r has no 'finished=' toggle line to derive the finished=1 variant" >&2
            fail=1
          fi
        done

        op() { printf '{"type":"spindrift_op","spindrift_op":%s}\n' "$1"; }
        started() { op "{\"op\":\"dispatch_start\",\"dispatch_start\":{\"driver\":\"$2\",\"started\":\"2026-11-14T$1.123456789''${3:-+01:00}\"}}"; }
        summary() { op "{\"op\":\"shadow_summary\",\"shadow_summary\":{\"driver\":\"$1\",\"loaded\":true,\"error\":\"\",\"compared\":5,\"diverged\":$2,\"forbidden\":0,\"skipped\":0}}"; }
        pass_start() { op '{"op":"pass_start","pass":1,"role":"implement"}'; }
        pass_usage() { op '{"op":"pass_usage","pass":1,"usage":{}}'; }
        no_outcome() { op '{"op":"pass_no_outcome","pass":1,"reason":"exit 1"}'; }
        settled() { op "{\"op\":\"dispatch_settled\",\"dispatch_settled\":{\"outcome\":\"''${1:-complete}\"}}"; }
        noise() { echo 'not json at all'; echo '{"type":"assistant"}'; }

        # claude log shapes: <file> <HH:MM:SS> ...
        c_clean() { { started "$2" claude "$4"; noise; pass_start; summary claude 0; pass_usage; [ -z "$3" ] || settled; } > "$1"; }
        c_missing() { { started "$2" claude "$3"; pass_start; pass_usage; no_outcome; } > "$1"; }
        c_unended() { { started "$2" claude; pass_start; op '{"op":"pass_model","model":"m","effort":"high"}'; } > "$1"; }
        c_killed() { { started "$2" claude; pass_start; settled failed; } > "$1"; }
        c_stampless() { { noise; pass_start; pass_usage; no_outcome; } > "$1"; }
        c_other() { { started "$2" opencode; pass_start; pass_usage; } > "$1"; }
        c_badstamp() { { started "$2" claude; pass_start; summary claude 0; pass_usage; settled; } > "$1"; }

        # github log shapes: <file> <summary diverged>
        g_clean() { summary github "''${2:-0}" > "$1"; noise >> "$1"; }
        g_none() { noise > "$1"; }

        run() { # <name> <recipe> <expected> <dir>
          actual=$(cd "$4" && bash "$OLDPWD/$2" 2>&1)
          if [ "$actual" = "$(printf '%s consecutive clean runs (cut-over bar: 20)' "$3")" ]; then
            echo "ok   $1: $3"
          else
            echo "FAIL $1: want $3, got: $actual"
            fail=1
          fi
        }
        mkcase() { rm -rf "case-$1"; mkdir -p "case-$1/.spindrift/logs"; echo "case-$1/.spindrift/logs"; }

        export LC_ALL=en_US.UTF-8
        probe=$(printf '2026-11-14T10:00:00+01:00\tclean\n\tdirty\n' | sort -r | head -n1)
        if [ "$probe" != "$(printf '\tdirty')" ]; then
          echo "FAIL harness: en_US.UTF-8 sort -r did not put the stamp-less line first: $probe"
          exit 1
        fi

        d=$(mkcase c1)
        c_clean "$d/issue-10.log" 10:00:00 settled
        c_clean "$d/issue-10-fix-1.log" 11:00:00
        c_clean "$d/issue-10-conflict-resolve.log" 12:00:00
        c_other "$d/issue-11.log" 13:00:00
        run claude-all-clean claude.sh 3 case-c1

        d=$(mkcase c2)
        c_clean "$d/issue-1.log" 09:00:00 settled
        c_missing "$d/issue-2.log" 10:00:00
        c_clean "$d/issue-3.log" 11:00:00 settled
        c_clean "$d/issue-4.log" 12:00:00 settled
        run claude-dirty-ends-streak claude.sh 2 case-c2

        d=$(mkcase c3)
        c_clean "$d/issue-1.log" 09:00:00 settled
        c_clean "$d/issue-2.log" 10:00:00 settled
        c_unended "$d/issue-3.log" 11:00:00
        run claude-in-flight-newest claude.sh 2 case-c3

        d=$(mkcase c4)
        c_clean "$d/issue-1.log" 09:00:00 settled
        c_clean "$d/issue-2.log" 10:00:00 settled
        c_stampless "$d/issue-3.log"
        run claude-stampless-dirty claude.sh 2 case-c4

        d=$(mkcase c5)
        c_clean "$d/issue-1.log" 09:00:00 settled
        c_killed "$d/issue-2.log" 10:00:00
        c_clean "$d/issue-3.log" 11:00:00 settled
        c_clean "$d/issue-4.log" 12:00:00 settled
        run claude-killed-mid-pass-ends-streak claude.sh 2 case-c5

        d=$(mkcase c6)
        c_clean "$d/issue-1.log" 09:00:00 settled
        c_clean "$d/issue-2.log" 10:00:00 settled
        c_unended "$d/issue-2-fix-1.log" 10:30:00
        c_clean "$d/issue-3.log" 11:00:00 settled
        c_clean "$d/issue-4.log" 12:00:00 settled
        run claude-killed-satellite-ends-streak claude.sh 2 case-c6

        d=$(mkcase c7)
        c_clean "$d/issue-1.log" 09:00:00 settled
        c_clean "$d/issue-2.log" 10:00:00 settled
        c_unended "$d/issue-3.log" 11:00:00
        c_unended "$d/issue-3-fix-1.log" 11:30:00
        c_unended "$d/issue-3-conflict-resolve.log" 12:00:00
        run claude-satellite-of-unsettled-primary-in-flight claude.sh 2 case-c7

        # Text order and time order disagree: 11:00+01:00 is 10:00Z, older than
        # the dirty 10:30Z run, so only a recipe ordering by instant counts 0.
        d=$(mkcase c8)
        c_clean "$d/issue-1.log" 09:00:00 settled +01:00
        c_clean "$d/issue-2.log" 11:00:00 settled +01:00
        c_missing "$d/issue-3.log" 10:30:00 Z
        run claude-mixed-offsets-time-order claude.sh 0 case-c8

        # A primary that never settled (launcher SIGKILLed, artifact still
        # uploaded) is in flight on a live host but dirty in fetched logs.
        d=$(mkcase c9)
        c_clean "$d/issue-1.log" 09:00:00 settled
        c_clean "$d/issue-2.log" 10:00:00 settled
        c_unended "$d/issue-3.log" 11:00:00
        run claude-finished-unsettled-primary-dirty claude-finished.sh 0 case-c9
        run claude-unsettled-primary-in-flight claude.sh 2 case-c9

        d=$(mkcase c10)
        c_clean "$d/issue-1.log" 09:00:00 settled
        c_clean "$d/issue-2.log" 10:00:00 settled
        c_unended "$d/issue-5-fix-1.log" 11:00:00
        run claude-finished-satellite-without-primary-dirty claude-finished.sh 0 case-c10
        run claude-satellite-without-primary-in-flight claude.sh 2 case-c10

        # A malformed stamp must not drop the log: it sorts last like a missing
        # one and keeps its clean verdict, so the count is 3, not 2.
        d=$(mkcase c11)
        c_clean "$d/issue-1.log" 09:00:00 settled
        c_clean "$d/issue-2.log" 10:00:00 settled
        c_badstamp "$d/issue-3.log" xx
        run claude-malformed-stamp-keeps-verdict claude.sh 3 case-c11

        # The settled lookup strips -fix-N from the base name, so a logs path
        # holding "-fix-" still finds the primary (killed, dirty) log.
        mkcase c12 >/dev/null
        mkdir -p case-c12/x-fix-y
        c_clean case-c12/x-fix-y/issue-1.log 09:00:00 settled
        c_clean case-c12/x-fix-y/issue-2.log 10:00:00 settled
        c_killed case-c12/x-fix-y/issue-3.log 11:00:00
        sed 's#^logs=.*#logs=case-c12/x-fix-y#' claude.sh > claude-fixdir.sh
        run claude-fix-in-logs-path claude-fixdir.sh 0 .

        stamp() { echo "shadow-github-$1-$2.log"; }
        dead_pid=999999999 # above Linux's pid_max (4194304), so never a live process

        d=$(mkcase g1)
        g_clean "$d/$(stamp 20261114T090000Z $dead_pid)"
        g_clean "$d/$(stamp 20261114T100000Z $dead_pid)"
        g_clean "$d/$(stamp 20261114T110000Z $dead_pid)"
        run github-all-clean github.sh 3 case-g1

        d=$(mkcase g2)
        g_clean "$d/$(stamp 20261114T090000Z $dead_pid)"
        g_clean "$d/$(stamp 20261114T100000Z $dead_pid)" 2
        g_clean "$d/$(stamp 20261114T110000Z $dead_pid)"
        g_clean "$d/$(stamp 20261114T120000Z $dead_pid)"
        run github-diverged-ends-streak github.sh 2 case-g2

        d=$(mkcase g3)
        g_clean "$d/$(stamp 20261114T090000Z $dead_pid)"
        g_none "$d/$(stamp 20261114T100000Z $dead_pid)"
        g_clean "$d/$(stamp 20261114T110000Z $dead_pid)"
        g_clean "$d/$(stamp 20261114T120000Z $dead_pid)"
        run github-dead-no-summary-ends-streak github.sh 2 case-g3

        d=$(mkcase g4)
        g_clean "$d/$(stamp 20261114T090000Z $dead_pid)"
        g_clean "$d/$(stamp 20261114T100000Z $dead_pid)"
        g_none "$d/$(stamp 20261114T110000Z $$)"
        run github-in-flight-newest github.sh 2 case-g4

        d=$(mkcase g5)
        g_clean "$d/$(stamp 20261114T090000Z $dead_pid)"
        g_clean "$d/$(stamp 20261114T100000Z $dead_pid)"
        g_none "$d/$(stamp 20261114T110000Z $dead_pid)"
        run github-dead-no-summary-newest github.sh 0 case-g5

        d=$(mkcase g6)
        g_clean "$d/$(stamp 20261114T090000Z $dead_pid)"
        g_clean "$d/$(stamp 20261114T100000Z $dead_pid)"
        g_none "$d/$(stamp 20261114T110000Z $$)"
        run github-finished-no-summary-dirty github-finished.sh 0 case-g6

        [ "$fail" = 0 ] || exit 1
        touch "$out"
      '';
}
