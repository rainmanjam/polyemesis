package db

import (
	"database/sql"
	"encoding/json"
	"errors"
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
		r                  alerts.Rule
		enabled            int
		eventsJSON         string
		created, updated   int64
		format, minSeverit string
	)
	if err := s.Scan(&r.ID, &r.Name, &enabled, &r.URL, &format, &eventsJSON,
		&minSeverit, &r.DebounceSeconds, &r.MinIntervalSeconds, &created, &updated); err != nil {
		return nil, err
	}
	r.Enabled = enabled != 0
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
	debounce_seconds, min_interval_seconds, created_at, updated_at`

// ListAlertRules returns every rule, oldest first.
func (d *DB) ListAlertRules() ([]alerts.Rule, error) {
	rows, err := d.sql.Query(`SELECT ` + alertRuleColumns + ` FROM alert_rules ORDER BY id`)
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
	rows, err := d.sql.Query(`SELECT ` + alertRuleColumns + ` FROM alert_rules WHERE enabled = 1 ORDER BY id`)
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
	r, err := scanAlertRule(d.sql.QueryRow(`SELECT `+alertRuleColumns+` FROM alert_rules WHERE id = ?`, id))
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
	events, err := marshalTypes(norm.Events)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	res, err := d.sql.Exec(`INSERT INTO alert_rules
		(name, enabled, url, format, events, min_severity, debounce_seconds, min_interval_seconds, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		norm.Name, boolToInt(norm.Enabled), norm.URL, string(norm.Format), events,
		string(norm.MinSeverity), norm.DebounceSeconds, norm.MinIntervalSeconds, now, now)
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
	if err != nil {
		return nil, err
	}
	res, err := d.sql.Exec(`UPDATE alert_rules SET
		name=?, enabled=?, url=?, format=?, events=?, min_severity=?,
		debounce_seconds=?, min_interval_seconds=?, updated_at=? WHERE id=?`,
		norm.Name, boolToInt(norm.Enabled), norm.URL, string(norm.Format), events,
		string(norm.MinSeverity), norm.DebounceSeconds, norm.MinIntervalSeconds,
		time.Now().Unix(), norm.ID)
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
