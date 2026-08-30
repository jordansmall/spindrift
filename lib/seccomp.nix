# Compiles a curated syscall DENYLIST into a raw BPF filter file consumable
# by bwrap's `--seccomp FD` (issue #2670 slice 1). bwrap reads the fd's
# entire contents as a bare array of `struct sock_filter` (8 bytes each) --
# no length header, no sock_fprog envelope -- and requires `len % 8 == 0`
# (bubblewrap's seccomp_program_new dies otherwise). libseccomp's
# seccomp_export_bpf writes exactly that same raw array, so the compiled
# output here can be handed straight to bwrap with no translation step.
#
# This is a DENYLIST, not a podman-style full allowlist: enumerating every
# syscall the whole agent toolchain (an arbitrary Driver, arbitrary Consumer
# build tooling) might need is unverifiable without exhaustively testing
# every combination, and a false negative in an allowlist silently breaks a
# Dispatch. A denylist can only ever be too narrow -- a documented gap to
# close later -- never break a working Box outright.
#
# clone/unshare/setns/personality are deliberately excluded: bare clone(2)
# backs ordinary thread creation everywhere, and safely scoping a deny to
# just namespace-creating flag combinations (or specific personality values)
# needs argument-aware BPF rules this first cut doesn't attempt.
{ pkgs }:
let
  deniedSyscalls = [
    "acct"
    "add_key"
    "bpf"
    "clock_adjtime"
    "clock_settime"
    "create_module"
    "delete_module"
    "finit_module"
    "get_kernel_syms"
    "init_module"
    "ioperm"
    "iopl"
    "kexec_file_load"
    "kexec_load"
    "keyctl"
    "lookup_dcookie"
    "mount"
    "nfsservctl"
    "open_by_handle_at"
    "perf_event_open"
    "pivot_root"
    "process_vm_readv"
    "process_vm_writev"
    "ptrace"
    "query_module"
    "reboot"
    "request_key"
    "settimeofday"
    "stime"
    "swapoff"
    "swapon"
    "syslog"
    "umount2"
    "ustat"
    "vm86"
    "vm86old"
  ];

  denylistCArray = builtins.concatStringsSep ", " (
    map (name: ''"${name}"'') deniedSyscalls
  );

  generatorSrc = pkgs.writeText "spindrift-seccomp-gen.c" ''
    #include <errno.h>
    #include <fcntl.h>
    #include <seccomp.h>
    #include <stdio.h>
    #include <stdlib.h>
    #include <unistd.h>

    static const char *const denylist[] = { ${denylistCArray} };

    int main(int argc, char **argv) {
      if (argc != 2) {
        fprintf(stderr, "usage: %s <output-path>\n", argv[0]);
        return 1;
      }

      scmp_filter_ctx ctx = seccomp_init(SCMP_ACT_ALLOW);
      if (ctx == NULL) {
        fprintf(stderr, "seccomp_init failed\n");
        return 1;
      }

      size_t n = sizeof(denylist) / sizeof(denylist[0]);
      for (size_t i = 0; i < n; i++) {
        int sysno = seccomp_syscall_resolve_name(denylist[i]);
        if (sysno == __NR_SCMP_ERROR) {
          /* Doesn't exist on this arch/kernel -- not an error. */
          continue;
        }
        int rc = seccomp_rule_add(ctx, SCMP_ACT_ERRNO(EPERM), sysno, 0);
        if (rc != 0) {
          fprintf(stderr, "seccomp_rule_add(%s) failed: %d\n", denylist[i], rc);
          exit(1);
        }
      }

      int fd = open(argv[1], O_WRONLY | O_CREAT | O_TRUNC, 0644);
      if (fd < 0) {
        fprintf(stderr, "open(%s) failed\n", argv[1]);
        return 1;
      }

      int rc = seccomp_export_bpf(ctx, fd);
      if (rc != 0) {
        fprintf(stderr, "seccomp_export_bpf failed: %d\n", rc);
        return 1;
      }

      close(fd);
      seccomp_release(ctx);
      return 0;
    }
  '';
in
pkgs.stdenv.mkDerivation {
  name = "spindrift-syscall-filter.bpf";
  dontUnpack = true;
  buildInputs = [ pkgs.libseccomp ];
  buildPhase = ''
    $CC -O2 -Wall -Wextra -o gen ${generatorSrc} -lseccomp
    ./gen filter.bpf
  '';
  installPhase = ''
    cp filter.bpf $out
  '';
}
