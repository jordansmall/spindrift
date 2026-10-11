# Option paths declared outside lib/structural-paths.nix: byName, plus the
# driver map's drivers and defaultDriver.
import ./byname-paths.nix // (import ./driver-map.nix).optionPaths
