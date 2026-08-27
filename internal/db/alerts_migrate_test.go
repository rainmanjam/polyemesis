package db

import "testing"

// An install that predates the alert-rule SSRF guard must gain the column on
// open, keep the rules it already had, and default them to refusing private
// targets.
//
// Every other alert-rule test builds a fresh database from schema.sql, where
// CREATE TABLE puts the column there and the gap is invisible -- so the whole
// suite would pass while an upgraded install answered "no such column:
// allow_private_target" on its first rule read, with the alerts page empty and
// every alert silently undelivered. The same trap MigrateHookAllowPrivateTarget
// was written for, one table over (#607).
func TestAnUpgradedInstallGainsTheAlertRuleAllowPrivateTargetColumn(t *testing.T) {
	d := testDB(t)

	// A rule written BEFORE the column existed. Created through the normal path
	// so it is a real row, then the column is taken away to reproduce the shape
	// of a database from the previous release.
	if _, err := d.CreateAlertRule(validRule()); err != nil {
		t.Fatalf("create alert rule: %v", err)
	}
	if _, err := d.sql.Exec(`ALTER TABLE alert_rules DROP COLUMN allow_private_target`); err != nil {
		t.Fatalf("could not reproduce the old schema: %v", err)
	}
	if has, err := columnExists(d.sql, "alert_rules", "allow_private_target"); err != nil || has {
		t.Fatalf("the column is still there, so this test proves nothing (has=%v err=%v)", has, err)
	}
	// The state an upgrade actually starts from: reading rules must fail.
	if _, err := d.ListAlertRules(); err == nil {
		t.Fatal("reading alert rules on the old schema succeeded; the premise of this test is wrong")
	}

	if err := d.MigrateAlertRuleAllowPrivateTarget(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	list, err := d.ListAlertRules()
	if err != nil {
		t.Fatalf("alert rules unreadable after the migration: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("the migration lost rows: %d rules, want 1", len(list))
	}
	if list[0].AllowPrivateTarget {
		t.Error("an existing rule came back allowed to reach private targets; " +
			"an upgrade must keep refusing them until an operator opts in")
	}
	// Idempotent: every later open runs this again.
	if err := d.MigrateAlertRuleAllowPrivateTarget(); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

// A rule stored before the guard existed, pointing at a private address, must
// still LOAD. Refusing it at read time would empty the alerts page and stop
// every OTHER rule with it -- a working install taken down by the guard that
// was supposed to protect it. It is refused at the notifier's dial instead, so
// the operator sees one rule failing with a message naming the opt-in.
func TestAnAlertRuleStoredBeforeTheGuardStillLoads(t *testing.T) {
	d := testDB(t)

	// Written straight to the table: CreateAlertRule validates, which is
	// exactly the door this row did not come through.
	if _, err := d.sql.Exec(`INSERT INTO alert_rules
		(name, enabled, url, format, events, min_severity, debounce_seconds,
		 min_interval_seconds, allow_private_target, created_at, updated_at)
		VALUES ('legacy',1,'http://169.254.169.254/','json','[]','info',10,30,0,0,0)`); err != nil {
		t.Fatalf("seed the legacy row: %v", err)
	}

	list, err := d.ListAlertRules()
	if err != nil {
		t.Fatalf("ListAlertRules refused to load a legacy private-target rule: %v -- "+
			"that takes the whole alerts page down, not just the one rule", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d rules, want the legacy one", len(list))
	}
	if list[0].AllowPrivateTarget {
		t.Error("the legacy rule loaded WITH the opt-in set; it must load without " +
			"one, so the dial-time guard still refuses to send to it")
	}
	// And it is refused the moment anybody tries to save it again, which is
	// where an operator is told what to do about it.
	if _, err := d.UpdateAlertRule(&list[0]); err == nil {
		t.Error("UpdateAlertRule re-saved a private-target rule with no opt-in")
	}
}

// The opt-in has to survive the round trip through the table. If it did not,
// an operator who deliberately allowed a self-hosted endpoint would find the
// rule saved, the box ticked in the response, and every delivery refused after
// the next restart -- a guard that looks off and behaves on.
func TestAlertRuleAllowPrivateTargetRoundTrips(t *testing.T) {
	d := testDB(t)

	r := validRule()
	r.URL = "http://10.1.2.3:9000/alerts"
	r.AllowPrivateTarget = true
	created, err := d.CreateAlertRule(r)
	if err != nil {
		t.Fatalf("CreateAlertRule refused a private target that HAD the opt-in: %v", err)
	}
	if !created.AllowPrivateTarget {
		t.Fatal("the opt-in did not survive CreateAlertRule")
	}

	got, err := d.GetAlertRule(created.ID)
	if err != nil {
		t.Fatalf("GetAlertRule: %v", err)
	}
	if !got.AllowPrivateTarget {
		t.Fatal("the opt-in did not survive a read; every delivery to this rule " +
			"would be refused after a restart with nothing to explain why")
	}

	// And turning it back off must stick, which is the direction that re-arms
	// the guard -- and the URL has to move with it or Validate refuses.
	got.AllowPrivateTarget = false
	got.URL = "https://hooks.example.com/services/T0/B1/xxxx"
	off, err := d.UpdateAlertRule(got)
	if err != nil {
		t.Fatalf("UpdateAlertRule: %v", err)
	}
	if off.AllowPrivateTarget {
		t.Error("clearing the opt-in did not stick; the operator cannot re-arm the guard")
	}
}
