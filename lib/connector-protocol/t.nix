# The type DSL of the Connector protocol description (default.nix). A type is an
# attrset with a `kind` that render.nix turns into a JSON Schema (draft 2020-12)
# node; nothing here evaluates a schema itself. Plain `let`, not `rec`, because
# the `null` entry would shadow the builtin inside a recursive set.
let
  mk = kind: extra: { inherit kind; } // extra;
in
{
  string = mk "string" { };
  integer = mk "integer" { };
  number = mk "number" { };
  boolean = mk "boolean" { };
  null = mk "null" { };
  # Any JSON value: the empty schema.
  any = mk "any" { };
  list = items: mk "list" { inherit items; };
  # An object of arbitrary string keys, every value `values`.
  map = values: mk "map" { inherit values; };
  enum = values: mk "enum" { inherit values; };
  # A reference to a shared type in `types` of default.nix.
  ref = name: mk "ref" { inherit name; };
  # `x` or null.
  nullable = inner: mk "nullable" { inherit inner; };
  # An object field that may be absent; valid only as an `object`/`open` field.
  optional = inner: mk "optional" { inherit inner; };
  # An object that admits no field but the ones named: what core sends in
  # params, and what it decodes strictly.
  object = fields: mk "object" { inherit fields; };
  # An object that tolerates fields it does not name: a result core decodes
  # leniently.
  open = fields: mk "open" { inherit fields; };
  # A raw method's params: the bytes after "<method>\n", described in prose.
  raw = doc: mk "raw" { inherit doc; };
}
