package canaryutils

import (
	"context"
	"errors"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// CheckPostgresSettings reconnects and verifies the requested server settings.
// Parameter names are bound as query arguments; credentials and setting values
// are never included in errors. PostgreSQL byte/time units and boolean aliases
// are compared by value so equivalent settings such as 32MB and 32768kB match.
func CheckPostgresSettings(ctx context.Context, connectionString string, settings map[string]string) error {
	if len(settings) == 0 {
		return nil
	}
	config, err := verifiedPostgresConfig(connectionString)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	connection, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return postgresError("settings connect", err)
	}
	defer closePostgres(connection)
	names := make([]string, 0, len(settings))
	for name := range settings {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var actual, unit, kind string
		err := connection.QueryRow(ctx, `SELECT current_setting($1), COALESCE((SELECT unit FROM pg_settings WHERE name=$1), ''), COALESCE((SELECT vartype FROM pg_settings WHERE name=$1), '')`, name).Scan(&actual, &unit, &kind)
		if err != nil {
			return postgresError("read configured setting", err)
		}
		if !matchingPostgresSetting(actual, settings[name], unit, kind) {
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
	return validA && validB && categoryA == categoryB && math.Abs(a-b) <= math.Max(1, math.Max(math.Abs(a), math.Abs(b)))*1e-9
}

var settingQuantity = regexp.MustCompile(`^([+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+))\s*([A-Za-z]*)$`)

func postgresQuantity(value, defaultUnit string) (float64, string, bool) {
	match := settingQuantity.FindStringSubmatch(value)
	if match == nil {
		return 0, "", false
	}
	number, err := strconv.ParseFloat(match[1], 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, "", false
	}
	unit := match[2]
	if unit == "" && defaultUnit != "" {
		// Some pg_settings units are multiples, such as 8kB blocks.
		base := defaultUnit
		if !settingQuantity.MatchString(base) {
			base = "1" + base
		}
		factor, category, valid := postgresQuantity(base, "")
		return number * factor, category, valid
	}
	type quantityUnit struct {
		factor   float64
		category string
	}
	units := map[string]quantityUnit{
		"": {1, "number"}, "b": {1, "bytes"}, "kb": {1024, "bytes"}, "mb": {1024 * 1024, "bytes"},
		"gb": {1024 * 1024 * 1024, "bytes"}, "tb": {1024 * 1024 * 1024 * 1024, "bytes"},
		"us": {0.000001, "seconds"}, "ms": {0.001, "seconds"}, "s": {1, "seconds"}, "min": {60, "seconds"}, "h": {3600, "seconds"}, "d": {86400, "seconds"},
	}
	scale, exists := units[strings.ToLower(unit)]
	return number * scale.factor, scale.category, exists
}
