package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/rainmanjam/polyemesis/internal/alerts"
)

// Alert rules are stored as alerts.Rule rather than a row struct of their own,
// the same way a destination stores a routing.Profile: the domain package owns
// the validation and the defaults, and a second copy of the type here would be
// one more place for them to disagree.

// scanAlertRule reads one row.
func scanAlertRule(s interface{ Scan(...any) error }) (*alerts.Rule, error) {
	var (
		r                     alerts.Rule
		enabled, allowPrivate int
		eventsJSON            string
		created, updated      int64
		format, minSeverit    string
	)
	if err := s.Scan(&r.ID, &r.Name, &enabled, &r.URL, &format, &eventsJSON,
		&minSeverit, &r.DebounceSeconds, &r.MinIntervalSeconds, &allowPrivate,
		&created, &updated); err != nil {
		return nil, err
	}
	r.Enabled = enabled != 0
	// Read, never re-validated. A rule stored before the SSRF guard existed
	// that points at a LAN address still LOADS: refusing it here would empty
	// the alert-rules page and stop every other rule with it, which is a
	// working install taken down by a guard that was supposed to protect it.
	// It is refused at the point that matters instead -- the notifier's dial --
	// so the operator sees one rule failing with a message naming the opt-in,
	// and the other rules keep alerting. See #607.
	r.AllowPrivateTarget = allowPrivate != 0
	r.Format = alerts.Format(format)
	r.MinSeverity = alerts.Severity(minSeverit)
	// A rule whose subscription list will not parse subscribes to everything.
	// The alternative — dropping the rule — is a webhook that has silently
	// stopped alerting, which is the failure mode nobody notices until it
	// matters.
	if eventsJSON != "" && eventsJSON != "[]" {
		var list []alerts.Type
		if err := json.Unmarshal([]byte(eventsJSON), &list); err == nil {
			r.Events = list
		}
	}
	r.CreatedAt = time.Unix(created, 0)
	r.UpdatedAt = time.Unix(updated, 0)
	out := r.Normalized()
	return &out, nil
}

const alertRuleColumns = `id, name, enabled, url, format, events, min_severity,
	debounce_seconds, min_interval_seconds, allow_private_target, created_at, updated_at`

// The alert-rule reads, as whole compile-time constants.
//
// Go folds `"a" + constB + "c"` at compile time when every operand is a const,
// so these cost nothing at runtime and cannot vary. That is the point: a query
// assembled at the call site is indistinguishable, to a reader and to a static
// analyser, from one that interpolates a variable. Making the query a constant
// makes it safe BY CONSTRUCTION — there is no expression left for a value to
// reach. See the same treatment, and the fuller argument, in chat.go.
const (
	alertRulesQuery        = `SELECT ` + alertRuleColumns + ` FROM alert_rules ORDER BY id`
	alertRulesEnabledQuery = `SELECT ` + alertRuleColumns + ` FROM alert_rules WHERE enabled = 1 ORDER BY id`
	alertRuleByIDQuery     = `SELECT ` + alertRuleColumns + ` FROM alert_rules WHERE id = ?`
)

// ListAlertRules returns every rule, oldest first.
func (d *DB) ListAlertRules() ([]alerts.Rule, error) {
	rows, err := d.sql.Query(alertRulesQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []alerts.Rule{}
	for rows.Next() {
		r, err := scanAlertRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// AlertRules returns the enabled rules. It is what satisfies
// alerts.RuleSource, so the notifier never sees a rule that is switched off.
func (d *DB) AlertRules() ([]alerts.Rule, error) {
	rows, err := d.sql.Query(alertRulesEnabledQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []alerts.Rule{}
	for rows.Next() {
		r, err := scanAlertRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// GetAlertRule loads one rule.
func (d *DB) GetAlertRule(id int64) (*alerts.Rule, error) {
	r, err := scanAlertRule(d.sql.QueryRow(alertRuleByIDQuery, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// CreateAlertRule stores a new rule.
func (d *DB) CreateAlertRule(r *alerts.Rule) (*alerts.Rule, error) {
	norm := r.Normalized()
	if err := norm.Validate(); err != nil {
		return nil, err
	}
	// A NAME THAT ALREADY EXISTS IS REFUSED, because the list is the only thing
	// telling two rules apart. An operator with two rules called "disk" cannot
	// see which is which, and the one they switch off may not be the one that
	// has been firing.
	//
	// Checked here rather than with a UNIQUE index: the comparison is case- and
	// space-folded ("Disk " and "disk" are indistinguishable on screen, which
	// is the whole harm) and SQLite's NOCASE collation is ASCII-only.
	existing, err := d.ListAlertRules()
	if err != nil {
		return nil, err
	}
	if err := alerts.CheckNameUnique(norm, existing); err != nil {
		return nil, err
	}
	events, err := marshalTypes(norm.Events)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	res, err := d.sql.Exec(`INSERT INTO alert_rules
		(name, enabled, url, format, events, min_severity, debounce_seconds, min_interval_seconds, allow_private_target, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		norm.Name, boolToInt(norm.Enabled), norm.URL, string(norm.Format), events,
		string(norm.MinSeverity), norm.DebounceSeconds, norm.MinIntervalSeconds,
		boolToInt(norm.AllowPrivateTarget), now, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return d.GetAlertRule(id)
}

// UpdateAlertRule replaces a rule in place.
func (d *DB) UpdateAlertRule(r *alerts.Rule) (*alerts.Rule, error) {
	norm := r.Normalized()
	if err := norm.Validate(); err != nil {
		return nil, err
	}
	events, err := marshalTypes(norm.Events)
	// Same refusal on the way through an edit; CheckNameUnique excludes the
	// rule's own id, so re-saving one unchanged is not a conflict with itself.
	// A NAME THAT ALREADY EXISTS IS REFUSED, because the list is the only thing
	// telling two rules apart. An operator with two rules called "disk" cannot
	// see which is which, and the one they switch off may not be the one that
	// has been firing.
	//
	// Checked here rather than with a UNIQUE index: the comparison is case- and
	// space-folded ("Disk " and "disk" are indistinguishable on screen, which
	// is the whole harm) and SQLite's NOCASE collation is ASCII-only.
	existing, err := d.ListAlertRules()
	if err != nil {
		return nil, err
	}
	if err := alerts.CheckNameUnique(norm, existing); err != nil {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	res, err := d.sql.Exec(`UPDATE alert_rules SET
		name=?, enabled=?, url=?, format=?, events=?, min_severity=?,
		debounce_seconds=?, min_interval_seconds=?, allow_private_target=?, updated_at=? WHERE id=?`,
		norm.Name, boolToInt(norm.Enabled), norm.URL, string(norm.Format), events,
		string(norm.MinSeverity), norm.DebounceSeconds, norm.MinIntervalSeconds,
		boolToInt(norm.AllowPrivateTarget), time.Now().Unix(), norm.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return d.GetAlertRule(norm.ID)
}

// SetAlertRuleEnabled flips one rule without touching the rest of it.
func (d *DB) SetAlertRuleEnabled(id int64, enabled bool) error {
	res, err := d.sql.Exec(`UPDATE alert_rules SET enabled=?, updated_at=? WHERE id=?`,
		boolToInt(enabled), time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAlertRule removes a rule.
func (d *DB) DeleteAlertRule(id int64) error {
	res, err := d.sql.Exec(`DELETE FROM alert_rules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MigrateAlertRuleAllowPrivateTarget adds alert_rules.allow_private_target to a
// database created before the alert-rule SSRF guard existed.
//
// WHY THIS EXISTS AT ALL, since schema.sql already declares the column: that
// file is CREATE TABLE IF NOT EXISTS, so on an install whose alert_rules table
// is already there it does nothing. The column arrived with the guard (#607)
// and every alert-rule read now names it, so without this an upgraded install
// answers "no such column: allow_private_target" on the first rule query and
// keeps doing it -- the alerts page empty, every alert silently undelivered,
// and nothing in the schema to suggest why. This is the same trap
// MigrateHookAllowPrivateTarget was written for, one table over.
//
// DEFAULT 0, so an upgraded install keeps refusing private targets. The safe
// direction: an operator who wants one opts in deliberately, exactly as a new
// install would. A rule already pointing at a private address keeps loading and
// stops DELIVERING -- see scanAlertRule for why that is the right way round.
func (d *DB) MigrateAlertRuleAllowPrivateTarget() error {
	// Checked before any transaction opens, for the reason
	// MigrateDestinationExpertArgs records: db.go sets SetMaxOpenConns(1), so a
	// read issued while a transaction holds the one connection waits for a
	// connection that transaction will not release, and startup hangs for ever.
	has, err := columnExists(d.sql, "alert_rules", "allow_private_target")
	if err != nil {
		return fmt.Errorf("inspect alert_rules columns: %w", err)
	}
	if has {
		return nil
	}
	if _, err := d.sql.Exec(
		`ALTER TABLE alert_rules ADD COLUMN allow_private_target INTEGER NOT NULL DEFAULT 0`,
	); err != nil {
		return fmt.Errorf("add alert_rules.allow_private_target: %w", err)
	}
	return nil
}

// marshalTypes encodes a subscription list. An empty list is stored as "[]",
// which means "every event", so the column is never NULL.
func marshalTypes(list []alerts.Type) (string, error) {
	if len(list) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(list)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
