# The nix preamble (prepended by mkHarness.nix) exports all baked config into
# the process environment.  The Go binary reads them via os.Getenv, loads any
# $PWD/harness.env overrides, and drives the full launch sequence.
exec spindrift-run
