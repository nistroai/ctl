package buildinfo

import "testing"

func TestString(t *testing.T) {
	version, commit, date := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = version, commit, date })
	Version, Commit, Date = "v1.2.3", "abc123", "2026-10-19T00:00:00Z"
	if got, want := String(), "v1.2.3 (commit abc123, built 2026-10-19T00:00:00Z)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
