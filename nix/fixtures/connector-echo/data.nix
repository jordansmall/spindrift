# The test-only `echo` Connector as data: a name and nothing else, since it
# declares no row of its own. Nothing registers it; the entry carries `data` so
# every `lib.connector` entry registers as data.
_: {
  name = "echo";
}
