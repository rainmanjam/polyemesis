# Fixtures for the backgrounded-step rule

These are NOT workflows. They live under `testdata/` so GitHub never reads them
and so the repository's own AST walkers skip them.

`red/` holds files the rule in `../../workflowtimeout_test.go` MUST flag, one
per way of getting it wrong. `green/` holds files it must NOT flag, one per way
of being fine.

They exist because a guard nobody has watched fail is a guard nobody has
evidence works — the lesson `scripts/sbom-guard.sh` was written to record, and
the reason eight tests in this repository have shipped passing for the wrong
reason. Adding a case to the rule means adding a red fixture for it here.
