# Lives in its own file so the runner-names-guard exemption covers only this
# check, not the rest of schema-drift.nix (issue #5804).
{ pkgs, ... }:
{
  # A runtime listed by two runner rows is ambiguous (ADR 0069), so
  # lib/runners/index.nix must throw when either attribute is forced. The
  # control proves the real rows evaluate. The literal runtime and family
  # lists below are a deliberate golden for the "RUNTIME choices unchanged"
  # acceptance of #5678: they pin the enum order and family mapping
  # independently of the rows they come from, so they name runtimes on purpose.
  runners-unique-runtime =
    let
      inherit (pkgs.lib) assertMsg;
      rows = import ../../lib/runners;
      index = rs: import ../../lib/runners/index.nix { rows = rs; };
      duplicated = rows ++ [
        {
          name = "dup";
          runtimes = [ firstRuntime ];
          artifact = "oci-image";
        }
      ];
      real = import ../../lib/runners/index.nix { };
      # The duplicate must collide with a real runtime to trigger the throw.
      firstRuntime = builtins.head real.runtimes;
      bad = index duplicated;
      badArtifact = index (map (r: r // { artifact = "oci-imag"; }) rows);
    in
    assert assertMsg
      (
        real.runtimes == [
          "podman"
          "docker"
          "rancher"
          "bwrap"
        ]
      )
      "runners-unique-runtime (ADR 0069): the real rows' runtime union must be podman docker rancher bwrap in that order";
    assert assertMsg
      (
        map (rt: (real.forRuntime rt).name) [
          "podman"
          "docker"
          "rancher"
          "bwrap"
        ] == [
          "oci"
          "oci"
          "oci"
          "bwrap"
        ]
      )
      "runners-unique-runtime (ADR 0069): forRuntime must map podman/docker/rancher to oci and bwrap to bwrap";
    assert assertMsg
      (
        (real.forRuntime "podman").artifact == "oci-image"
        && (real.forRuntime "bwrap").artifact == "store-closure"
      )
      "runners-unique-runtime (ADR 0069): the oci row must carry the oci-image artifact and bwrap the store-closure artifact";
    assert assertMsg (!(builtins.tryEval bad.runtimes).success)
      "runners-unique-runtime (ADR 0069): two rows listing the same runtime must fail eval of `runtimes`, but it evaluated";
    assert assertMsg (!(builtins.tryEval (bad.forRuntime firstRuntime)).success)
      "runners-unique-runtime (ADR 0069): two rows listing the same runtime must fail eval of `forRuntime`, but it evaluated";
    assert assertMsg (!(builtins.tryEval badArtifact.runtimes).success)
      "runners-unique-runtime (ADR 0069): a row with an unknown artifact must fail eval of `runtimes`, but it evaluated";
    assert assertMsg (!(builtins.tryEval (badArtifact.forRuntime firstRuntime)).success)
      "runners-unique-runtime (ADR 0069): a row with an unknown artifact must fail eval of `forRuntime`, but it evaluated";
    assert assertMsg (!(builtins.tryEval (builtins.deepSeq badArtifact.rows badArtifact.rows)).success)
      "runners-unique-runtime (ADR 0069): a row with an unknown artifact must fail eval of `rows`, but it evaluated";
    assert assertMsg (!(builtins.tryEval (builtins.deepSeq bad.rows bad.rows)).success)
      "runners-unique-runtime (ADR 0069): two rows listing the same runtime must fail eval of `rows`, but it evaluated";
    assert assertMsg (!(builtins.tryEval (real.forRuntime "nerdctl")).success)
      "runners-unique-runtime (ADR 0069): a runtime no row lists must make `forRuntime` throw, but it returned a row";
    pkgs.runCommand "runners-unique-runtime" { } "touch $out";
}
