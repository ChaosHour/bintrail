package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestExtractGrantUser(t *testing.T) {
	tests := []struct {
		name  string
		grant string
		want  string
	}{
		{
			name:  "simple user@host with single quotes",
			grant: "GRANT USAGE ON *.* TO 'bintrail'@'%'",
			want:  "'bintrail'@'%'",
		},
		{
			name:  "user@host with localhost",
			grant: "GRANT SELECT ON db.* TO 'app'@'localhost'",
			want:  "'app'@'localhost'",
		},
		{
			name:  "trailing IDENTIFIED BY clause (older MySQL)",
			grant: "GRANT REPLICATION SLAVE ON *.* TO 'repl'@'10.0.0.5' IDENTIFIED BY '<secret>'",
			want:  "'repl'@'10.0.0.5'",
		},
		{
			name:  "WITH GRANT OPTION suffix",
			grant: "GRANT ALL PRIVILEGES ON *.* TO 'admin'@'%' WITH GRANT OPTION",
			want:  "'admin'@'%'",
		},
		{
			name:  "lowercase TO is uppercased by ToUpper search",
			grant: "grant select on db.* to 'app'@'localhost'",
			want:  "'app'@'localhost'",
		},
		{
			name:  "backtick-quoted identifier",
			grant: "GRANT USAGE ON *.* TO `bintrail`@`%`",
			want:  "`bintrail`@`%`",
		},
		{
			name:  "no TO clause",
			grant: "REVOKE ALL PRIVILEGES",
			want:  "",
		},
		{
			name:  "empty",
			grant: "",
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractGrantUser(tt.grant)
			if got != tt.want {
				t.Errorf("extractGrantUser(%q) = %q, want %q", tt.grant, got, tt.want)
			}
		})
	}
}

func TestDeriveServerID(t *testing.T) {
	const dsn = "user:pass@tcp(source.example.com:3306)/mydb"
	id1, err := deriveServerID(dsn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	id2, err := deriveServerID(dsn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id1 != id2 {
		t.Errorf("deriveServerID is not deterministic: %d vs %d", id1, id2)
	}

	// Range invariant: must be >= 100M to keep distance from the 1-1000 zone
	// most production replicas use. The PR-review caught a typo that broke
	// this — keep the assertion strict.
	if id1 < 100000000 {
		t.Errorf("deriveServerID produced %d, expected >= 100000000", id1)
	}

	// Distinct DSNs produce distinct IDs. With 100 generated inputs the
	// probability of any collision in a 4.2B range is < 10^-15 — this catches
	// regressions like a stub returning a constant, or a hash truncation bug
	// that compresses into a 16-bit space.
	seen := make(map[uint32]string, 100)
	for i := range 100 {
		gen := fmt.Sprintf("u:p@tcp(host%d.example.com:3306)/db", i)
		id, err := deriveServerID(gen)
		if err != nil {
			t.Fatalf("deriveServerID(%q) error: %v", gen, err)
		}
		if id < 100000000 {
			t.Errorf("deriveServerID(%q) = %d, below floor 100000000", gen, id)
		}
		if prev, dup := seen[id]; dup {
			t.Errorf("collision: %q and %q both produced %d", prev, gen, id)
		}
		seen[id] = gen
	}

	// Bad DSN returns an error rather than silently substituting a
	// non-deterministic value (the silent-failure fix).
	if _, err := deriveServerID("not-a-dsn"); err == nil {
		t.Error("expected error for unparseable DSN; got nil")
	}
}

func TestDoctorReportAdd(t *testing.T) {
	r := &doctorReport{}
	r.add(checkResult{Name: "a", Status: statusPass})
	r.add(checkResult{Name: "b", Status: statusFail})
	r.add(checkResult{Name: "c", Status: statusWarn})
	r.add(checkResult{Name: "d", Status: statusSkip})
	r.add(checkResult{Name: "e", Status: statusPass})

	if r.Passed != 2 || r.Failed != 1 || r.Warnings != 1 || r.Skipped != 1 {
		t.Errorf("counts wrong: %+v", r)
	}
	if len(r.Checks) != 5 {
		t.Errorf("expected 5 checks, got %d", len(r.Checks))
	}
}

func TestDoctorReportAddUnknownStatusDoesNotCountButAppends(t *testing.T) {
	r := &doctorReport{}
	r.add(checkResult{Name: "weird", Status: checkStatus("UNKNOWN")})
	if len(r.Checks) != 1 {
		t.Errorf("expected unknown check appended for JSON visibility, got %d entries", len(r.Checks))
	}
	if r.Passed+r.Failed+r.Warnings+r.Skipped != 0 {
		t.Errorf("expected no counters incremented for unknown status, got %+v", r)
	}
}

func TestDoctorReportErr(t *testing.T) {
	// No failures → nil error (warnings are tolerated).
	r := &doctorReport{Passed: 3, Warnings: 2}
	if err := r.Err(); err != nil {
		t.Errorf("expected nil error with no failures, got %v", err)
	}

	// One failure → error.
	r2 := &doctorReport{Passed: 3, Failed: 1}
	err := r2.Err()
	if err == nil {
		t.Error("expected error when Failed > 0")
	}
	if err != nil && !strings.Contains(err.Error(), "1 preflight check") {
		t.Errorf("error message does not mention check count: %v", err)
	}
}

func TestDoctorReportWriteJSON(t *testing.T) {
	// Build a report, marshal via Write, unmarshal, assert deep equality.
	// Catches: missing JSON tags, dropped fields, type mismatches.
	in := &doctorReport{
		Checks: []checkResult{
			{Name: "ok", Status: statusPass, Detail: "MySQL 8.0.36"},
			{Name: "bad", Status: statusFail, Detail: "denied", Remediation: "GRANT X ON *.* ..."},
		},
		Passed: 1,
		Failed: 1,
	}
	var buf bytes.Buffer
	if err := in.Write(&buf, "json"); err != nil {
		t.Fatalf("Write(json) error: %v", err)
	}
	var out doctorReport
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, buf.String())
	}
	if out.Passed != 1 || out.Failed != 1 {
		t.Errorf("counters lost in round-trip: %+v", out)
	}
	if len(out.Checks) != 2 {
		t.Fatalf("expected 2 checks, got %d", len(out.Checks))
	}
	if out.Checks[0].Status != statusPass {
		t.Errorf("status round-trip failed: %q", out.Checks[0].Status)
	}
	if out.Checks[1].Remediation != "GRANT X ON *.* ..." {
		t.Errorf("remediation lost: %q", out.Checks[1].Remediation)
	}
}

func TestCheckLogBin(t *testing.T) {
	// The check has unique string-comparison logic (not delegating to an
	// existing validator) so it is the highest-value sqlmock target — a
	// regression here would silently PASS on a server with binary logging off.
	tests := []struct {
		name       string
		returnVal  string
		queryErr   error
		wantStatus checkStatus
		wantDetail string
	}{
		{name: "ON via 1", returnVal: "1", wantStatus: statusPass, wantDetail: "ON"},
		{name: "ON via literal", returnVal: "ON", wantStatus: statusPass, wantDetail: "ON"},
		{name: "ON case-insensitive", returnVal: "on", wantStatus: statusPass, wantDetail: "ON"},
		{name: "OFF literal", returnVal: "OFF", wantStatus: statusFail, wantDetail: `log_bin="OFF"`},
		{name: "OFF via 0", returnVal: "0", wantStatus: statusFail, wantDetail: `log_bin="0"`},
		{name: "empty string", returnVal: "", wantStatus: statusFail, wantDetail: `log_bin=""`},
		{name: "query error", queryErr: errors.New("denied"), wantStatus: statusFail, wantDetail: "denied"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()

			exp := mock.ExpectQuery("SELECT @@log_bin")
			if tt.queryErr != nil {
				exp.WillReturnError(tt.queryErr)
			} else {
				exp.WillReturnRows(sqlmock.NewRows([]string{"@@log_bin"}).AddRow(tt.returnVal))
			}

			got := checkLogBin(db)
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tt.wantStatus)
			}
			if !strings.Contains(got.Detail, tt.wantDetail) {
				t.Errorf("Detail = %q, want substring %q", got.Detail, tt.wantDetail)
			}
			// FAIL outcomes must carry remediation so the user has a path forward;
			// exception: query errors (detail itself is the remediation hint).
			if tt.wantStatus == statusFail && tt.queryErr == nil && got.Remediation == "" {
				t.Error("FAIL outcome with no remediation breaks doctor's promise")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
		})
	}
}

func TestCheckBinlogRetention(t *testing.T) {
	// Three logical branches plus error paths:
	//   1. MySQL 8.0+ via @@binlog_expire_logs_seconds (>= threshold, below, zero, unparseable)
	//   2. Legacy MySQL 5.7 fallback via @@expire_logs_days
	//   3. Both queries error (warn-only, doctor proceeds)
	tests := []struct {
		name              string
		modern            sql8Response // first query — @@binlog_expire_logs_seconds
		legacy            sql8Response // second query — @@expire_logs_days (only invoked when modern errors)
		wantStatus        checkStatus
		wantDetailFrag    string // substring assertion on detail
		wantRemediation   bool   // must remediation be present?
	}{
		// 1. MySQL 8.0+ branches.
		{name: "modern: at threshold", modern: row("172800"), wantStatus: statusPass, wantDetailFrag: "48h"},
		{name: "modern: above threshold", modern: row("259200"), wantStatus: statusPass, wantDetailFrag: "72h"},
		{name: "modern: below threshold", modern: row("3600"), wantStatus: statusWarn, wantDetailFrag: "1h", wantRemediation: true},
		{name: "modern: zero (never expire)", modern: row("0"), wantStatus: statusWarn, wantDetailFrag: "no automatic expiration"},
		{name: "modern: unparseable", modern: row("not-an-int"), wantStatus: statusWarn, wantDetailFrag: "could not parse"},
		// 2. Legacy fallback when modern errors.
		{name: "legacy: 7 days", modern: errResp("unknown variable"), legacy: row("7"), wantStatus: statusPass, wantDetailFrag: "7 days"},
		{name: "legacy: 1 day", modern: errResp("unknown variable"), legacy: row("1"), wantStatus: statusWarn, wantDetailFrag: "expire_logs_days=1", wantRemediation: true},
		{name: "legacy: unparseable", modern: errResp("unknown variable"), legacy: row("garbage"), wantStatus: statusWarn, wantDetailFrag: "could not parse"},
		// 3. Both error → warn-only (no remediation; doctor proceeds with degraded info).
		{name: "both error", modern: errResp("conn lost"), legacy: errResp("conn lost"), wantStatus: statusWarn, wantDetailFrag: "could not read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()

			expect := mock.ExpectQuery("SELECT @@binlog_expire_logs_seconds")
			tt.modern.apply(expect, "@@binlog_expire_logs_seconds")
			if tt.modern.err != nil {
				lexpect := mock.ExpectQuery("SELECT @@expire_logs_days")
				tt.legacy.apply(lexpect, "@@expire_logs_days")
			}

			got := checkBinlogRetention(db)
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q (detail=%q)", got.Status, tt.wantStatus, got.Detail)
			}
			if !strings.Contains(got.Detail, tt.wantDetailFrag) {
				t.Errorf("Detail = %q, want substring %q", got.Detail, tt.wantDetailFrag)
			}
			if tt.wantRemediation && got.Remediation == "" {
				t.Error("expected remediation but got none")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
		})
	}
}

// sql8Response is a tiny helper to make the binlog-retention table-driven test
// readable: each row says "for this query, return either a value or an error."
type sql8Response struct {
	value string
	err   error
}

func row(v string) sql8Response       { return sql8Response{value: v} }
func errResp(msg string) sql8Response { return sql8Response{err: errors.New(msg)} }

func (r sql8Response) apply(exp *sqlmock.ExpectedQuery, col string) {
	if r.err != nil {
		exp.WillReturnError(r.err)
		return
	}
	exp.WillReturnRows(sqlmock.NewRows([]string{col}).AddRow(r.value))
}

func TestCheckIndexWriteAccessOn(t *testing.T) {
	const dbName = "binlog_index"

	// Each subtest sets up the sqlmock expectation chain that mirrors the
	// branch under test. The probe table name is fixed in checkIndexWriteAccessOn
	// as `binlog_index`.`_bintrail_doctor_probe`.
	tests := []struct {
		name           string
		setup          func(mock sqlmock.Sqlmock)
		wantStatus     checkStatus
		wantDetailFrag string
	}{
		{
			name: "db exists, create+drop OK",
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectQuery("SELECT SCHEMA_NAME FROM information_schema.SCHEMATA").
					WillReturnRows(sqlmock.NewRows([]string{"SCHEMA_NAME"}).AddRow(dbName))
				m.ExpectExec("CREATE TABLE IF NOT EXISTS").WillReturnResult(sqlmock.NewResult(0, 0))
				m.ExpectExec("DROP TABLE").WillReturnResult(sqlmock.NewResult(0, 0))
			},
			wantStatus:     statusPass,
			wantDetailFrag: "CREATE/DROP TABLE OK",
		},
		{
			name: "db missing, create database succeeds, then create+drop OK",
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectQuery("SELECT SCHEMA_NAME FROM information_schema.SCHEMATA").
					WillReturnRows(sqlmock.NewRows([]string{"SCHEMA_NAME"}))
				m.ExpectExec("CREATE DATABASE IF NOT EXISTS").WillReturnResult(sqlmock.NewResult(0, 0))
				m.ExpectExec("CREATE TABLE IF NOT EXISTS").WillReturnResult(sqlmock.NewResult(0, 0))
				m.ExpectExec("DROP TABLE").WillReturnResult(sqlmock.NewResult(0, 0))
			},
			wantStatus:     statusPass,
			wantDetailFrag: "CREATE/DROP TABLE OK",
		},
		{
			name: "db missing, create database denied",
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectQuery("SELECT SCHEMA_NAME FROM information_schema.SCHEMATA").
					WillReturnRows(sqlmock.NewRows([]string{"SCHEMA_NAME"}))
				m.ExpectExec("CREATE DATABASE IF NOT EXISTS").
					WillReturnError(errors.New("Access denied for user"))
			},
			wantStatus:     statusFail,
			wantDetailFrag: "cannot CREATE DATABASE",
		},
		{
			name: "db exists, create table denied",
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectQuery("SELECT SCHEMA_NAME FROM information_schema.SCHEMATA").
					WillReturnRows(sqlmock.NewRows([]string{"SCHEMA_NAME"}).AddRow(dbName))
				m.ExpectExec("CREATE TABLE IF NOT EXISTS").
					WillReturnError(errors.New("CREATE command denied"))
			},
			wantStatus:     statusFail,
			wantDetailFrag: "cannot CREATE TABLE",
		},
		{
			name: "create OK but drop denied — must FAIL (catches partition-rotate bites at runtime)",
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectQuery("SELECT SCHEMA_NAME FROM information_schema.SCHEMATA").
					WillReturnRows(sqlmock.NewRows([]string{"SCHEMA_NAME"}).AddRow(dbName))
				m.ExpectExec("CREATE TABLE IF NOT EXISTS").WillReturnResult(sqlmock.NewResult(0, 0))
				m.ExpectExec("DROP TABLE").WillReturnError(errors.New("DROP command denied"))
			},
			wantStatus:     statusFail,
			wantDetailFrag: "user has CREATE but not DROP",
		},
		{
			name: "schemata query errors",
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectQuery("SELECT SCHEMA_NAME FROM information_schema.SCHEMATA").
					WillReturnError(errors.New("conn lost"))
			},
			wantStatus:     statusFail,
			wantDetailFrag: "conn lost",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()

			tt.setup(mock)

			got := checkIndexWriteAccessOn(t.Context(), db, dbName)
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q (detail=%q)", got.Status, tt.wantStatus, got.Detail)
			}
			if !strings.Contains(got.Detail, tt.wantDetailFrag) {
				t.Errorf("Detail = %q, want substring %q", got.Detail, tt.wantDetailFrag)
			}
			// All FAIL outcomes from this check must carry remediation — the user
			// needs a concrete next action (GRANT statement, manual CREATE DATABASE, etc.).
			if tt.wantStatus == statusFail && got.Remediation == "" {
				t.Error("FAIL outcome with no remediation breaks doctor's promise")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet expectations: %v", err)
			}
		})
	}
}

func TestDoctorReportWriteText(t *testing.T) {
	// Text formatter emits a specific status glyph per status. Any UI scraper
	// or grep workflow depends on this contract — assert per-glyph.
	r := &doctorReport{}
	r.add(checkResult{Name: "p", Status: statusPass, Detail: "ok"})
	r.add(checkResult{Name: "f", Status: statusFail, Detail: "bad", Remediation: "fix it"})
	r.add(checkResult{Name: "w", Status: statusWarn, Detail: "meh"})
	r.add(checkResult{Name: "s", Status: statusSkip, Detail: "n/a"})

	var buf bytes.Buffer
	if err := r.Write(&buf, "text"); err != nil {
		t.Fatalf("Write(text): %v", err)
	}
	out := buf.String()
	for _, want := range []string{"✓ p", "✗ f", "! w", "- s", "    fix it"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q\n--- output ---\n%s", want, out)
		}
	}
}
