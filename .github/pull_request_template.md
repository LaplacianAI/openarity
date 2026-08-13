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
touches storage, and a real terminal if it touches what the CLI prints.
Paste the output, not a description of it.
-->

```sh
cd apps/brain
make check db=postgres

cd apps/cli
make check
```

## Checklist

- [ ] `make check` passes in every module touched — with a database for the brain
- [ ] Tests cover the new behaviour, including at least one failure case
- [ ] Each new guard was broken to confirm a test fails without it
- [ ] Migrations, if any, have a `Down` and were applied and rolled back
- [ ] The spec and the generated client were regenerated together, if either changed
- [ ] No secret, credential or DSN with a password appears in the diff
- [ ] Documentation updated if behaviour or configuration changed
