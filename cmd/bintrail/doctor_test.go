package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
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
