- CI is not yours to fetch: the CI FAILURE section above, when the launcher
  captured one, carries each failing check and a tail of its job's log. Run no
  forge CLI or API call for CI. A check with no log excerpt only names what
  failed — reproduce it locally by running that check.
