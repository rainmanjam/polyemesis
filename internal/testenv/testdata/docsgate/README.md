# Fixtures for the documentation-gate rule

These are NOT workflows. They live under `testdata/` so GitHub never reads them
and so the repository's own AST walkers skip them.

`red/` holds files the rule in `../../docsgate_test.go` MUST flag, one per way
of getting it wrong. `green/` holds files it must NOT flag, one per way of
being fine — including the shape that looks wrong and is not: a job-level `if:`
reading some *other* job's output, which is what `container` legitimately does.

The failure this rule prevents cannot be reproduced on a laptop. It needs a
branch-protection ruleset, a required matrix context and a documentation-only
pull request, and it presents as a green pull request that will not merge. So
the only evidence that the rule works is watching it fail on something, and
that is what these are. Adding a case to the rule means adding a red fixture
for it here.
