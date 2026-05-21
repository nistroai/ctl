package postgres

import "testing"

func TestParseGiB(t *testing.T) {
	tests := []struct {
		value string
		want  float64
	}{
		{"2Gi", 2},
		{"2", 2},
		{"50Gi", 50},
		{"50", 50},
	}
	for _, tt := range tests {
		got, err := parseGiB(tt.value)
		if err != nil {
			t.Fatalf("parseGiB(%q): %v", tt.value, err)
		}
		if got != tt.want {
			t.Fatalf("parseGiB(%q) = %v, want %v", tt.value, got, tt.want)
		}
	}
}

func TestParseGiBRejectsNonPositive(t *testing.T) {
	for _, value := range []string{"0", "-1", ""} {
		if _, err := parseGiB(value); err == nil {
			t.Fatalf("parseGiB(%q) succeeded, want error", value)
		}
	}
}
