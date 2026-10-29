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
    pkgs.runCommand "seccomp-filter-is-regular-file-multiple-of-8-bytes" { }
      ''
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

  # Runs the filter as a classic-BPF program against each probe, since the
  # file has no header to inspect. A native-only filter is BADARCH=KILL for
  # compat processes (issue #6139); a compat-only alias of a denied syscall
  # (umount, clock_*64) must not fall through to ALLOW. The EPERM probes rely
  # on mount/ptrace/etc. being in deniedSyscalls.
  seccomp-filter-covers-compat-architectures =
    let
      allow = "0x7fff0000";
      eperm = "0x00050001"; # SCMP_ACT_ERRNO(EPERM)
      kill = "0x00000000";
      probe = label: arch: nr: want: {
        inherit
          label
          arch
          nr
          want
          ;
      };
      # x32 shares AUDIT_ARCH_X86_64 and is told apart by nr|0x40000000.
      probes =
        if pkgs.stdenv.hostPlatform.isx86_64 then
          let
            x86_64 = "0xc000003e";
            i386 = "0x40000003";
          in
          [
            (probe "native getpid" x86_64 "39" allow)
            (probe "foreign arch getpid" "0xdeadbeef" "39" kill)
            (probe "native mount" x86_64 "165" eperm)
            (probe "x32 getpid" x86_64 "0x40000027" allow)
            (probe "x32 mount" x86_64 "0x400000a5" eperm)
            (probe "i386 getpid" i386 "20" allow)
            (probe "i386 mount" i386 "21" eperm)
            (probe "i386 ptrace" i386 "26" eperm)
            (probe "i386 umount" i386 "22" eperm)
            (probe "i386 clock_settime64" i386 "404" eperm)
            (probe "i386 clock_adjtime64" i386 "405" eperm)
          ]
        else if pkgs.stdenv.hostPlatform.isAarch64 then
          let
            aarch64 = "0xc00000b7";
            arm = "0x40000028";
          in
          [
            (probe "native getpid" aarch64 "172" allow)
            (probe "foreign arch getpid" "0xdeadbeef" "172" kill)
            (probe "native mount" aarch64 "40" eperm)
            (probe "arm getpid" arm "20" allow)
            (probe "arm mount" arm "21" eperm)
            (probe "arm ptrace" arm "26" eperm)
            (probe "arm clock_settime64" arm "404" eperm)
            (probe "arm clock_adjtime64" arm "405" eperm)
          ]
        else
          throw "seccomp-filter-covers-compat-architectures: unsupported host ${pkgs.stdenv.hostPlatform.system}";
    in
    pkgs.runCommand "seccomp-filter-covers-compat-architectures"
      {
        nativeBuildInputs = [ pkgs.python3 ];
        probes = builtins.toJSON probes;
      }
      ''
        python3 -I - ${seccompFilter} <<'EOF'
        import json, os, struct, sys
        prog = list(struct.iter_unpack("<HBBI", open(sys.argv[1], "rb").read()))

        def run(arch, nr):
            # seccomp_data: nr at offset 0, arch at offset 4, rest zero.
            data = {0: nr, 4: arch}
            a = pc = 0
            while True:
                code, jt, jf, k = prog[pc]
                pc += 1
                if code == 0x20:  # ld abs word
                    a = data.get(k, 0)
                elif code == 0x06:  # ret K
                    return k
                elif code == 0x05:  # ja
                    pc += k
                elif code in (0x15, 0x25, 0x35, 0x45):  # jeq/jgt/jge/jset K
                    hit = {
                        0x15: a == k,
                        0x25: a > k,
                        0x35: a >= k,
                        0x45: (a & k) != 0,
                    }[code]
                    pc += jt if hit else jf
                else:
                    sys.exit(f"unsupported BPF opcode {code:#x} at instruction {pc - 1}")

        failures = []
        for p in json.loads(os.environ["probes"]):
            want = int(p["want"], 0)
            got = run(int(p["arch"], 0), int(p["nr"], 0))
            if got != want:
                failures.append(f"{p['label']}: want {want:#010x}, got {got:#010x}")
        if failures:
            sys.exit("seccomp filter probe mismatches:\n" + "\n".join(failures))
        EOF
        touch $out
      '';
}
