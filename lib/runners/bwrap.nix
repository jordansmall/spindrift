{
  name = "bwrap";
  goName = "Bwrap";
  runtimes = [ "bwrap" ];
  artifact = "store-closure";
  # No "no-host-loopback": bwrap has no rendering distinct from "open" (issue #2666).
  networkModes = [
    "open"
    "none"
    "host"
  ];
}
