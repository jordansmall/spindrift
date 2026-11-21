# Issue #7041: the shadow-mode "consecutive clean runs" recipes in
# docs/reference.md are shell a human pastes, so nothing ran them. This
# extracts each fenced block from the doc as written and runs each over small
# fixture log sets, so doc and check cannot drift.
#
# Cases pin: a run still in flight is skipped, not counted dirty (an unended
# Pass whose Dispatch's primary `issue-<n>.log` has no `dispatch_settled` for
# opencode; a live file-name pid for github); a finished run with a missing
# summary is dirty, a killed `-fix-N` satellite included, since only the
# primary log is ever settled; and a stamp-less log never sorts ahead of
# stamped ones, which the opencode cases run under en_US.UTF-8 to catch (the
# locale ignores the leading tab, so a plain `sort -r` puts the empty stamp
# first; a malformed stamp sorts like a missing one but keeps its verdict).
# Stamps carry mixed UTC offsets, so they order by instant, not text. Each
# recipe's `finished=` toggle is also run set to 1, for logs fetched from CI,
# which have all ended: then an unended Pass or missing summary counts dirty.
#
# The shadow recipe also reads each log's `shadow-replay` sidecar (issue #6872):
# live and replayed summaries count together, a run counts once whichever
# holds its summaries, and each non-empty set must be clean on its own. A log
# stamped with another Driver stays skipped whatever sits beside it, and a
# sidecar not opening with its log's own dispatch_start is ignored.
#
# The primary-mode recipe (issue #6998) counts a process clean only with an
# audit file holding at least one decision entry (the sandbox totals line is
# not one) and no refusal, and an empty or absent `.log`; a live pid is in flight,
# and `finished=1` counts it by its content.
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
        extract driver=opencode > opencode.sh
        extract backend=github > github.sh
        extract primary=github > primary.sh
        fail=0
        for r in opencode.sh github.sh primary.sh; do
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

        # opencode log shapes: <file> <HH:MM:SS> ...
        log_clean() { { started "$2" opencode "$4"; noise; pass_start; summary opencode 0; pass_usage; [ -z "$3" ] || settled; } > "$1"; }
        log_missing() { { started "$2" opencode "$3"; pass_start; pass_usage; no_outcome; } > "$1"; }
        log_unended() { { started "$2" opencode; pass_start; op '{"op":"pass_model","model":"m","effort":"high"}'; } > "$1"; }
        log_killed() { { started "$2" opencode; pass_start; settled failed; } > "$1"; }
        log_stampless() { { noise; pass_start; pass_usage; no_outcome; } > "$1"; }
        log_other() { { started "$2" claude; pass_start; pass_usage; } > "$1"; }
        log_badstamp() { { started "$2" opencode; pass_start; summary opencode 0; pass_usage; settled; } > "$1"; }

        # Two ended Passes and no live summary: a run only a replay can judge.
        log_nolive() { { started "$2" opencode; pass_start; pass_usage; pass_start; pass_usage; settled; } > "$1"; }
        # Sidecar beside a log: <log> <diverged>... one replayed summary each.
        # It opens with the log's dispatch_start, as Replay writes it.
        sidecar() {
          s="''${1%.log}.shadow-replay.jsonl"
          grep -m1 '"op":"dispatch_start"' "$1" > "$s" || true
          shift
          for dv in "$@"; do summary opencode "$dv" | sed 's/"driver":"opencode"/&,"replayed":true/' >> "$s"; done
        }

        # github log shapes: <file> <summary diverged>
        g_clean() { summary github "''${2:-0}" > "$1"; noise >> "$1"; }
        g_none() { noise > "$1"; }

        run() { # <name> <recipe> <expected> <dir> [bar]
          actual=$(cd "$4" && bash "$OLDPWD/$2" 2>&1)
          if [ "$actual" = "$(printf '%s consecutive clean runs (%s bar: 20)' "$3" "''${5:-primary}")" ]; then
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
        log_clean "$d/issue-10.log" 10:00:00 settled
        log_clean "$d/issue-10-fix-1.log" 11:00:00
        log_clean "$d/issue-10-conflict-resolve.log" 12:00:00
        log_other "$d/issue-11.log" 13:00:00
        run opencode-all-clean opencode.sh 3 case-c1

        d=$(mkcase c2)
        log_clean "$d/issue-1.log" 09:00:00 settled
        log_missing "$d/issue-2.log" 10:00:00
        log_clean "$d/issue-3.log" 11:00:00 settled
        log_clean "$d/issue-4.log" 12:00:00 settled
        run opencode-dirty-ends-streak opencode.sh 2 case-c2

        d=$(mkcase c3)
        log_clean "$d/issue-1.log" 09:00:00 settled
        log_clean "$d/issue-2.log" 10:00:00 settled
        log_unended "$d/issue-3.log" 11:00:00
        run opencode-in-flight-newest opencode.sh 2 case-c3

        d=$(mkcase c4)
        log_clean "$d/issue-1.log" 09:00:00 settled
        log_clean "$d/issue-2.log" 10:00:00 settled
        log_stampless "$d/issue-3.log"
        run opencode-stampless-dirty opencode.sh 2 case-c4

        d=$(mkcase c5)
        log_clean "$d/issue-1.log" 09:00:00 settled
        log_killed "$d/issue-2.log" 10:00:00
        log_clean "$d/issue-3.log" 11:00:00 settled
        log_clean "$d/issue-4.log" 12:00:00 settled
        run opencode-killed-mid-pass-ends-streak opencode.sh 2 case-c5

        d=$(mkcase c6)
        log_clean "$d/issue-1.log" 09:00:00 settled
        log_clean "$d/issue-2.log" 10:00:00 settled
        log_unended "$d/issue-2-fix-1.log" 10:30:00
        log_clean "$d/issue-3.log" 11:00:00 settled
        log_clean "$d/issue-4.log" 12:00:00 settled
        run opencode-killed-satellite-ends-streak opencode.sh 2 case-c6

        d=$(mkcase c7)
        log_clean "$d/issue-1.log" 09:00:00 settled
        log_clean "$d/issue-2.log" 10:00:00 settled
        log_unended "$d/issue-3.log" 11:00:00
        log_unended "$d/issue-3-fix-1.log" 11:30:00
        log_unended "$d/issue-3-conflict-resolve.log" 12:00:00
        run opencode-satellite-of-unsettled-primary-in-flight opencode.sh 2 case-c7

        # Text order and time order disagree: 11:00+01:00 is 10:00Z, older than
        # the dirty 10:30Z run, so only a recipe ordering by instant counts 0.
        d=$(mkcase c8)
        log_clean "$d/issue-1.log" 09:00:00 settled +01:00
        log_clean "$d/issue-2.log" 11:00:00 settled +01:00
        log_missing "$d/issue-3.log" 10:30:00 Z
        run opencode-mixed-offsets-time-order opencode.sh 0 case-c8

        # A primary that never settled (launcher SIGKILLed, artifact still
        # uploaded) is in flight on a live host but dirty in fetched logs.
        d=$(mkcase c9)
        log_clean "$d/issue-1.log" 09:00:00 settled
        log_clean "$d/issue-2.log" 10:00:00 settled
        log_unended "$d/issue-3.log" 11:00:00
        run opencode-finished-unsettled-primary-dirty opencode-finished.sh 0 case-c9
        run opencode-unsettled-primary-in-flight opencode.sh 2 case-c9

        d=$(mkcase c10)
        log_clean "$d/issue-1.log" 09:00:00 settled
        log_clean "$d/issue-2.log" 10:00:00 settled
        log_unended "$d/issue-5-fix-1.log" 11:00:00
        run opencode-finished-satellite-without-primary-dirty opencode-finished.sh 0 case-c10
        run opencode-satellite-without-primary-in-flight opencode.sh 2 case-c10

        # A malformed stamp must not drop the log: it sorts last like a missing
        # one and keeps its clean verdict, so the count is 3, not 2.
        d=$(mkcase c11)
        log_clean "$d/issue-1.log" 09:00:00 settled
        log_clean "$d/issue-2.log" 10:00:00 settled
        log_badstamp "$d/issue-3.log" xx
        run opencode-malformed-stamp-keeps-verdict opencode.sh 3 case-c11

        # The settled lookup strips -fix-N from the base name, so a logs path
        # holding "-fix-" still finds the primary (killed, dirty) log.
        mkcase c12 >/dev/null
        mkdir -p case-c12/x-fix-y
        log_clean case-c12/x-fix-y/issue-1.log 09:00:00 settled
        log_clean case-c12/x-fix-y/issue-2.log 10:00:00 settled
        log_killed case-c12/x-fix-y/issue-3.log 11:00:00
        sed 's#^logs=.*#logs=case-c12/x-fix-y#' opencode.sh > opencode-fixdir.sh
        run opencode-fix-in-logs-path opencode-fixdir.sh 0 .

        # Replayed runs count with live ones, once each (issue #6872).
        d=$(mkcase r1)
        log_clean "$d/issue-1.log" 09:00:00 settled
        log_clean "$d/issue-2.log" 10:00:00 settled
        log_nolive "$d/issue-3.log" 11:00:00
        sidecar "$d/issue-3.log" 0 0
        run opencode-replay-only-counts opencode.sh 3 case-r1

        d=$(mkcase r2)
        log_clean "$d/issue-1.log" 09:00:00 settled
        log_nolive "$d/issue-2.log" 10:00:00
        sidecar "$d/issue-2.log" 0 3
        log_clean "$d/issue-3.log" 11:00:00 settled
        log_clean "$d/issue-4.log" 12:00:00 settled
        run opencode-replay-divergence-ends-streak opencode.sh 2 case-r2

        d=$(mkcase r3)
        log_clean "$d/issue-1.log" 09:00:00 settled
        sidecar "$d/issue-1.log" 0
        log_clean "$d/issue-2.log" 10:00:00 settled
        sidecar "$d/issue-2.log" 0
        run opencode-live-and-replay-count-once opencode.sh 2 case-r3
        log_clean "$d/issue-3.log" 11:00:00 settled
        run opencode-live-and-replay-then-live-only opencode.sh 3 case-r3

        d=$(mkcase r4)
        log_clean "$d/issue-1.log" 09:00:00 settled
        log_clean "$d/issue-2.log" 10:00:00 settled
        log_clean "$d/issue-3.log" 11:00:00 settled
        sidecar "$d/issue-3.log" 1
        run opencode-clean-live-diverged-replay-dirty opencode.sh 0 case-r4

        d=$(mkcase r5)
        log_clean "$d/issue-1.log" 09:00:00 settled
        log_clean "$d/issue-2.log" 10:00:00 settled
        log_other "$d/issue-3.log" 11:00:00
        sidecar "$d/issue-3.log" 0
        run opencode-other-driver-skipped-despite-sidecar opencode.sh 2 case-r5

        # One summary for two ended Passes in a finished run is a missing one.
        d=$(mkcase r6)
        log_clean "$d/issue-1.log" 09:00:00 settled
        log_clean "$d/issue-2.log" 10:00:00 settled
        log_nolive "$d/issue-3.log" 11:00:00
        sidecar "$d/issue-3.log" 0
        run opencode-short-replay-dirty opencode.sh 0 case-r6

        # A re-dispatched issue's log is rewritten, but run A's sidecar stays:
        # its diverged replay must not be credited to run B.
        d=$(mkcase r7)
        log_clean "$d/issue-1.log" 09:00:00 settled
        log_clean "$d/issue-2.log" 10:00:00 settled
        log_nolive "$d/issue-3.log" 11:00:00
        sidecar "$d/issue-3.log" 1
        log_clean "$d/issue-3.log" 12:00:00 settled
        run opencode-stale-sidecar-ignored opencode.sh 3 case-r7

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

        # primary record pair: <dir> <stamp> <pid> then the audit lines
        pstem() { echo "$1/primary-github-$2-$3"; }
        p_audit() { # <stem> <decision>...
          s=$1; shift
          : > "$s.audit.jsonl"
          for dec in "$@"; do
            printf '{"route":"GET /repos","method":"GET","status":200,"decision":"%s"}\n' "$dec" >> "$s.audit.jsonl"
          done
        }
        p_clean() { p_audit "$(pstem "$1" "$2" "$3")" allowed allowed; }
        # The sandbox totals line a process appends at exit: no decision key.
        p_totals() {
          echo '{"sandbox_start_ns":1,"sandbox_calls":2,"sandbox_host_calls":3,"sandbox_hop_ns":4}' >> "$(pstem "$1" "$2" "$3").audit.jsonl"
        }
        crun() { run "$1" "$2" "$3" "$4" cut-over; }

        d=$(mkcase p1)
        p_clean "$d" 20261114T090000Z $dead_pid
        p_clean "$d" 20261114T100000Z $dead_pid
        : > "$(pstem "$d" 20261114T100000Z $dead_pid).log" # an empty log is clean
        p_clean "$d" 20261114T110000Z $dead_pid
        crun primary-all-clean primary.sh 3 case-p1

        d=$(mkcase p2)
        p_clean "$d" 20261114T090000Z $dead_pid
        p_audit "$(pstem "$d" 20261114T100000Z $dead_pid)" allowed refused
        p_clean "$d" 20261114T110000Z $dead_pid
        p_clean "$d" 20261114T120000Z $dead_pid
        crun primary-refusal-ends-streak primary.sh 2 case-p2

        d=$(mkcase p3)
        p_clean "$d" 20261114T090000Z $dead_pid
        p_clean "$d" 20261114T100000Z $dead_pid
        echo '{"op":"Issue","error":"boom"}' > "$(pstem "$d" 20261114T100000Z $dead_pid).log"
        p_clean "$d" 20261114T110000Z $dead_pid
        crun primary-read-error-ends-streak primary.sh 1 case-p3

        # A process that made no Connector request proves nothing.
        d=$(mkcase p4)
        p_clean "$d" 20261114T090000Z $dead_pid
        p_audit "$(pstem "$d" 20261114T100000Z $dead_pid)"
        p_clean "$d" 20261114T110000Z $dead_pid
        crun primary-empty-audit-ends-streak primary.sh 1 case-p4

        # The sandbox totals line is not an entry: alone it proves nothing,
        # and after real entries it leaves the run clean.
        d=$(mkcase p4t)
        p_clean "$d" 20261114T090000Z $dead_pid
        p_audit "$(pstem "$d" 20261114T100000Z $dead_pid)"
        p_totals "$d" 20261114T100000Z $dead_pid
        p_clean "$d" 20261114T110000Z $dead_pid
        p_totals "$d" 20261114T110000Z $dead_pid
        crun primary-totals-only-audit-ends-streak primary.sh 1 case-p4t
        d=$(mkcase p4c)
        p_clean "$d" 20261114T090000Z $dead_pid
        p_totals "$d" 20261114T090000Z $dead_pid
        p_clean "$d" 20261114T100000Z $dead_pid
        p_totals "$d" 20261114T100000Z $dead_pid
        crun primary-totals-after-entries-counts primary.sh 2 case-p4c

        # A load failure leaves an error line and no audit file.
        d=$(mkcase p5)
        p_clean "$d" 20261114T090000Z $dead_pid
        echo '{"op":"Issue","error":"load"}' > "$(pstem "$d" 20261114T100000Z $dead_pid).log"
        p_clean "$d" 20261114T110000Z $dead_pid
        crun primary-log-without-audit-ends-streak primary.sh 1 case-p5

        # A live pid is in flight on a live host, but counts by content when
        # the logs are fetched (finished=1).
        d=$(mkcase p6)
        p_clean "$d" 20261114T090000Z $dead_pid
        p_clean "$d" 20261114T100000Z $dead_pid
        p_audit "$(pstem "$d" 20261114T110000Z $$)" allowed refused
        crun primary-in-flight-newest primary.sh 2 case-p6
        crun primary-finished-refusal-dirty primary-finished.sh 0 case-p6

        d=$(mkcase p7)
        p_clean "$d" 20261114T090000Z $dead_pid
        p_clean "$d" 20261114T100000Z $$
        crun primary-finished-clean-counts primary-finished.sh 2 case-p7

        [ "$fail" = 0 ] || exit 1
        touch "$out"
      '';
}
