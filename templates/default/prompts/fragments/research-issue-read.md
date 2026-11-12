- The subject issue's own body and its last-10-comment snapshot are
  already in the # ISSUE TEXT section below. Pull any parent/linked issue or
  PRD it references too with `driver-exec signal issue <ref>` (a number,
  `#number`, `owner/repo#number` for this repository, or a Jira
  `KEY-number`), which prints its title, state, labels, body and last 10
  comments. A ref outside this dispatch's tracker is refused; work from the
  snapshot and name any unread issue the verdict depends on in the verdict
  comment.
- Any prior research comment (look for the `<!-- spindrift-research -->`
  marker used below) inside that last-10-comment snapshot — read it before
  researching again so a re-run doesn't repeat prior findings. A prior
  research comment older than the snapshot's 10-comment window is not
  visible to this pass at all.
