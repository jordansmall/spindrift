2. Print exactly one line as your final output and stop — raw plain text, not
   wrapped in backticks, a code fence, or any other markdown formatting:

   SPINDRIFT_OUTCOME issue=${ISSUE_NUMBER} landing=${BRANCH} status=ready note=<short reason>

   The launcher applies `MERGE_MODE` after this line (push straight to the
   target branch on `immediate`; leave the branch as pushed on `manual`).
   Do not open a pull request and do not attempt to merge.
