<!--
Thanks for the pull request.

For anything larger than a bug fix, an issue first saves everyone time — see
CONTRIBUTING.md. If there is one, link it below.
-->

## What this changes

<!-- The problem, not just the diff. What was wrong, or what was missing? -->

## How you know it works

<!--
Which suite, and what you measured. This project cares about tests that measure
rather than assert — "routing_test passes" is weaker than "the third track
measures within 0.5 dB of the other two at the destination".

  make test
  go test ./... -race
  ./scripts/acceptance.sh
  ./scripts/acceptance-audio.sh        (audio routing)
  ./scripts/acceptance-failover.sh     (source switching)
  ./scripts/acceptance-docker.sh       (the shipped container)

If you added a check to a suite with a fixed-value guard, raise EXPECTED_CHECKS
to match — otherwise the guard stops meaning anything.
-->

## What you deliberately did not do

<!--
Optional but valued. Known gaps, cases left out, follow-up worth doing. Saying
so here is much better than someone finding it later.
-->

## Checklist

- [ ] `make fmt` and `make lint` are clean
- [ ] Tests pass, including `-race`
- [ ] Comments explain *why* — especially anything surprising, or an approach
      that was tried and rejected
- [ ] No secret can reach a log, an API response or an error string
- [ ] Video is still never re-encoded on a destination path
- [ ] Docs updated if behaviour or configuration changed
