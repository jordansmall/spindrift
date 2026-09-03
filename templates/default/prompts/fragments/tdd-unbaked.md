Work test-first, one slice at a time. Hard rule:

1. RED: write ONE failing test, run it, confirm it fails for the right reason.
   Never write implementation code before a failing test exists.
2. GREEN: minimal code to make that one test pass.
3. REFACTOR, then repeat.

Never batch: no tests up front, no all-tests-then-all-code.
One failing test, one change, at a time.
