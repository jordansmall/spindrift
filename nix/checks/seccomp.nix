# Eval/build-level pins for lib/seccomp.nix (issue #2670 slice 1): the
# compiled BPF filter is consumed by bwrap's `--seccomp FD`, which reads the
# fd's entire contents as a raw `struct sock_filter[]` with no length header
# or `sock_fprog` envelope and requires `len % 8 == 0` (bubblewrap's own
# seccomp_program_new dies otherwise). This check builds the derivation and
# asserts its output shape matches what bwrap demands, since a directory, an
# empty file, or a byte length that isn't a multiple of 8 would silently pass
# a `nix build` but fail at bwrap launch time.
{ pkgs, ... }:
let
  seccompFilter = import ../../lib/seccomp.nix { inherit pkgs; };
in
{
  seccomp-filter-is-regular-file-multiple-of-8-bytes =
    pkgs.runCommand "seccomp-filter-is-regular-file-multiple-of-8-bytes" { } ''
      if [ ! -f ${seccompFilter} ]; then
        echo "seccomp filter output is not a regular file: ${seccompFilter}" >&2
        exit 1
      fi
      size=$(stat -c %s ${seccompFilter})
      if [ "$size" -eq 0 ]; then
        echo "seccomp filter output is empty" >&2
        exit 1
      fi
      if [ $((size % 8)) -ne 0 ]; then
        echo "seccomp filter output size ($size bytes) is not a multiple of 8 (bwrap requires len % 8 == 0)" >&2
        exit 1
      fi
      touch $out
    '';
}
