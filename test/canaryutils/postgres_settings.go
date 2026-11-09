package canaryutils

import (
	"context"
	"errors"
	"math"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// PostgresSetting captures an effective server value and the metadata needed to
// compare its PostgreSQL units and boolean aliases with a requested value.
type PostgresSetting struct {
	Value string
	Unit  string
	Kind  string
}

// ReadPostgresSettings reconnects and captures the named effective settings.
// Names are sorted, deduplicated, and bound as query arguments. Errors omit
// credentials, names, and values; an empty name list does not connect.
func ReadPostgresSettings(ctx context.Context, connectionString string, names []string) (map[string]PostgresSetting, error) {
	settings := make(map[string]PostgresSetting, len(names))
	if len(names) == 0 {
		return settings, nil
	}
	config, err := verifiedPostgresConfig(connectionString)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	connection, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return nil, postgresError("settings connect", err)
	}
	defer closePostgres(connection)
	unique := make(map[string]struct{}, len(names))
	for _, name := range names {
		unique[name] = struct{}{}
	}
	ordered := make([]string, 0, len(unique))
	for name := range unique {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		var setting PostgresSetting
		err := connection.QueryRow(ctx, `SELECT current_setting($1), COALESCE((SELECT unit FROM pg_settings WHERE name=$1), ''), COALESCE((SELECT vartype FROM pg_settings WHERE name=$1), '')`, name).Scan(&setting.Value, &setting.Unit, &setting.Kind)
		if err != nil {
			return nil, postgresError("read configured setting", err)
		}
		settings[name] = setting
	}
	return settings, nil
}

// PostgresSettingMatches compares a snapshot with a requested value, accepting
// equivalent PostgreSQL byte/time units, scientific notation, and boolean
// aliases. Integer values compare exactly after normalization; real values
// allow rounding tolerance.
func PostgresSettingMatches(setting PostgresSetting, requested string) bool {
	return matchingPostgresSetting(setting.Value, requested, setting.Unit, setting.Kind)
}

// CheckPostgresSettings reconnects and verifies the requested server settings.
// Credentials and setting names and values are never included in errors.
func CheckPostgresSettings(ctx context.Context, connectionString string, settings map[string]string) error {
	names := make([]string, 0, len(settings))
	for name := range settings {
		names = append(names, name)
	}
	observed, err := ReadPostgresSettings(ctx, connectionString, names)
	if err != nil {
		return err
	}
	for name, requested := range settings {
		if !PostgresSettingMatches(observed[name], requested) {
			return errors.New("Postgres canary configured setting did not match its requested value")
		}
	}
	return nil
}

func matchingPostgresSetting(actual, requested, unit, kind string) bool {
	actual, requested = strings.TrimSpace(actual), strings.TrimSpace(requested)
	if actual == requested {
		return true
	}
	if kind == "bool" || kind == "enum" {
		aliases := map[string]string{"on": "true", "true": "true", "yes": "true", "1": "true", "off": "false", "false": "false", "no": "false", "0": "false"}
		a, b := aliases[strings.ToLower(actual)], aliases[strings.ToLower(requested)]
		if a != "" && b != "" {
			return a == b
		}
		return kind == "enum" && strings.EqualFold(actual, requested)
	}
	if kind != "integer" && kind != "real" {
		return false
	}
	a, categoryA, validA := postgresQuantity(actual, unit)
	b, categoryB, validB := postgresQuantity(requested, unit)
	if !validA || !validB || categoryA != categoryB {
		return false
	}
	if kind == "integer" {
		return a.Cmp(b) == 0
	}
	first, _ := a.Float64()
	second, _ := b.Float64()
	return !math.IsInf(first, 0) && !math.IsInf(second, 0) && math.Abs(first-second) <= math.Max(1, math.Max(math.Abs(first), math.Abs(second)))*1e-9
}

var settingQuantity = regexp.MustCompile(`^([+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?)\s*([A-Za-z]*)$`)

// Rational normalization preserves adjacent integers even when applying a unit
// multiplier produces values beyond float64's exact integer range.
func postgresQuantity(value, defaultUnit string) (*big.Rat, string, bool) {
	match := settingQuantity.FindStringSubmatch(value)
	if match == nil {
		return nil, "", false
	}
	number, valid := new(big.Rat).SetString(match[1])
	if !valid {
		return nil, "", false
	}
	unit := match[2]
	if unit == "" && defaultUnit != "" {
		// Some pg_settings units are multiples, such as 8kB blocks.
		base := defaultUnit
		if !settingQuantity.MatchString(base) {
			base = "1" + base
		}
		factor, category, valid := postgresQuantity(base, "")
		if !valid {
			return nil, "", false
		}
		return number.Mul(number, factor), category, true
	}
	type quantityUnit struct {
		numerator, denominator int64
		category               string
	}
	units := map[string]quantityUnit{
		"": {1, 1, "number"}, "b": {1, 1, "bytes"}, "kb": {1024, 1, "bytes"}, "mb": {1024 * 1024, 1, "bytes"},
		"gb": {1024 * 1024 * 1024, 1, "bytes"}, "tb": {1024 * 1024 * 1024 * 1024, 1, "bytes"},
		"us": {1, 1000000, "seconds"}, "ms": {1, 1000, "seconds"}, "s": {1, 1, "seconds"}, "min": {60, 1, "seconds"}, "h": {3600, 1, "seconds"}, "d": {86400, 1, "seconds"},
	}
	scale, exists := units[strings.ToLower(unit)]
	if !exists {
		return nil, "", false
	}
	factor := new(big.Rat).SetFrac64(scale.numerator, scale.denominator)
	return number.Mul(number, factor), scale.category, true
}
