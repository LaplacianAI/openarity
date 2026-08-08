<!--
The pull request title becomes the squashed commit subject, so write it as a
commit message: feat(brain): serve the API and webhook listeners
-->

## What this changes

<!-- One or two sentences. The diff shows what; say why. -->

## Why

<!-- The problem this solves. Link the issue if there is one: Closes #123 -->

## How it was verified

<!--
What you actually ran, not what should work. Include a database run if this
touches storage.
-->

```sh
cd apps/brain
make check
make cover
```

## Checklist

- [ ] `make check` and `make cover` pass locally
- [ ] Tests cover the new behaviour, including at least one failure case
- [ ] Migrations, if any, have a `Down` and were applied and rolled back
- [ ] No secret, credential or DSN with a password appears in the diff
- [ ] Documentation updated if behaviour or configuration changed
