- The subject issue's own body and its last-10-comment snapshot are
  already in the # ISSUE TEXT section below. Pull any parent/linked issue or
  PRD it references too with `driver-exec signal issue <ref>` (a number,
  `#number`, `owner/repo#number` for this repository, or a Jira
  `KEY-number`), which prints its title, state, labels, body and last 10
  comments. A ref outside this dispatch's tracker is refused; work from the
  snapshot and name any unread issue the change depends on in the
  SPINDRIFT_OUTCOME `note=` field.
- `git log -n 10 --oneline` — recent history.
