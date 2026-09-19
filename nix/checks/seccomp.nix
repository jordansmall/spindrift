# Eval/build-level pins for lib/seccomp.nix (issue #2670 slice 1). bwrap's
# `--seccomp FD` reads the fd as a raw `struct sock_filter[]` with no length
# header or `sock_fprog` envelope, and seccomp_program_new dies unless
# `len % 8 == 0`. A bad filter passes `nix build` and fails only at launch.
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
