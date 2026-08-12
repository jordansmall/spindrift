**Push failure — check the actual cause before reporting it.** Do not guess.
Run:

```
git diff origin/${BASE_BRANCH} -- '.github/workflows/'
```

- **No diff (phantom delta):** The pre-push rebase-and-retry above should have
  cleared this. If the push still fails, capture and report the actual push
  error output.
- **Genuine `.github/workflows/` change:** The agent's token intentionally
  lacks `workflow` scope — this is a deliberate security boundary. Do NOT
  attempt to acquire broader scope or route around it. Comment on the issue
  explaining what changes were made and why they require human review with
  `workflow` scope, then emit `status=blocked`.
- **Any other rejection:** Report the literal push error output. Never
  attribute a failure to a cause you have not verified.
