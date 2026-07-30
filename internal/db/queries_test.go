package db

import "testing"

// Every read in this package is a compile-time constant so that no value can
// reach the SQL text. That makes the queries safe by construction, but it does
// not make them CORRECT: a constant with a typo in a column name is still a
// constant, and it fails at run time, on whichever code path happens to use it.
//
// Two of the converted queries — ListDestinationsBySource and
// ListRenditionsBySource — have no test coverage at all, so the suite passing
// says nothing about them. Rather than test those two functions and leave the
// same hole open for the next query added, this prepares every one of them
// against the real schema.
//
// SQLite resolves table and column names at prepare time, so a misspelled
// column, a missing table or unbalanced SQL fails here without any rows being
// needed. Add a query constant, add it to this list.
func TestEveryQueryConstantPreparesAgainstTheSchema(t *testing.T) {
	d := testDB(t)

	queries := map[string]string{
		"alertRulesQuery":        alertRulesQuery,
		"alertRulesEnabledQuery": alertRulesEnabledQuery,
		"alertRuleByIDQuery":     alertRuleByIDQuery,

		"chatRecentQuery":            chatRecentQuery,
		"chatRecentForPlatformQuery": chatRecentForPlatformQuery,
		"chatByAuthorQuery":          chatByAuthorQuery,

		"destBySourceQuery": destBySourceQuery,
		"destListQuery":     destListQuery,
		"destByIDQuery":     destByIDQuery,

		"renditionBySourceQuery": renditionBySourceQuery,
		"renditionListQuery":     renditionListQuery,
		"renditionByIDQuery":     renditionByIDQuery,

		"sourceListQuery":    sourceListQuery,
		"sourceByIDQuery":    sourceByIDQuery,
		"sourceByTokenQuery": sourceByTokenQuery,

		"sessionListQuery": sessionListQuery,
		"sessionByIDQuery": sessionByIDQuery,

		"scheduleListQuery":    scheduleListQuery,
		"scheduleEnabledQuery": scheduleEnabledQuery,
		"scheduleByIDQuery":    scheduleByIDQuery,

		"jobByIDQuery":   jobByIDQuery,
		"jobActiveQuery": jobActiveQuery,

		"transcriptTracksQuery": transcriptTracksQuery,
	}

	for name, q := range queries {
		t.Run(name, func(t *testing.T) {
			if q == "" {
				t.Fatal("the constant is empty")
			}
			stmt, err := d.sql.Prepare(q)
			if err != nil {
				t.Fatalf("does not prepare against the schema: %v\n\nSQL:\n%s", err, q)
			}
			_ = stmt.Close()
		})
	}
}
