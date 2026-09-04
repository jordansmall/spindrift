Hunt every dimension. Do not stop at the first finding:

**SPEC** — Does the diff do exactly what issue #${ISSUE_NUMBER} asked, nothing
more? Is EVERY acceptance criterion satisfied? Flag scope creep and unrequested
behaviour changes as loudly as missing requirements.

**CORRECTNESS** — Try to break it. Walk the edge cases the author skipped: empty
/ nil / zero / boundary inputs, error and early-return paths, partial failure,
concurrency and ordering, off-by-one, resource leaks (unclosed files, goroutines,
processes), and every branch the tests do NOT exercise. Untested new logic
is a finding on its own. A pure relocation, refactor, or comment/doc change
whose behaviour is already covered under test is not a coverage defect —
note it under Non-blocking rather than Blocking.

**SECURITY** — This system feeds untrusted issue and comment text to an agent as
prompt input, handles live secrets, and runs shelled-out commands. Look hard for:
command / shell / SQL injection and unquoted expansions, prompt-injection or
trust-boundary crossings, secret or token leakage into logs / args / error text,
widened token scope or permission surface, path traversal, and unsafe handling of
external input. Assume every external string is hostile.

**STANDARDS & SMELLS** — Does it follow the repo's documented standards, test
conventions, and commit style — whatever document the repo records them in?
Grep that document for the rule the diff implicates and read only that
section — do not read the whole document fresh each pass. If you compose a
subagent prompt for this dimension (e.g. when driving `/code-review`'s
Standards axis), carry the same grep-don't-read-whole rule into that prompt
too. Then hunt code smells: duplication, dead or unreachable code,
copy-paste drift, leaky or misplaced abstractions, misleading names,
swallowed errors, magic values, comments that lie, comment-to-code
disproportion, and anything that will rot. Nits count — surface them, don't
sit on them.
