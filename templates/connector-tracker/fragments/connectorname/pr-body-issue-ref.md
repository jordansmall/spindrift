3. Body MUST contain `connectorname-issue: ${ISSUE_NUMBER}` — a plain
   breadcrumb, never an auto-closing reference: a connectorname id is not a
   GitHub issue number, and an auto-close keyword would silently close an
   unrelated GitHub issue on the Target repo. Summarize what changed and flag
   anything a reviewer should know.
