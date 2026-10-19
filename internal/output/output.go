package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
)

// JSON preserves the entire API response. Tables favor stable summary fields.
func Print(w io.Writer, raw json.RawMessage, format string) error {
	if format == "json" {
		var v any
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		if err := decoder.Decode(&v); err != nil {
			return err
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	value = redactDetails(value)
	if obj, ok := value.(map[string]any); ok {
		for _, key := range []string{"deployments", "teams", "members", "snapshots", "accounts", "customNetworks", "networks", "invoices", "events", "deployment_options"} {
			if list, ok := obj[key].([]any); ok {
				value = list
				break
			}
		}
		if dep, ok := obj["deployment"].(map[string]any); ok {
			value = dep
		}
	}
	if list, ok := value.([]any); ok {
		if len(list) == 0 {
			_, err := fmt.Fprintln(w, "No results.")
			return err
		}
		var keys []string
		for _, key := range []string{"id", "name", "email", "model_name", "status", "cloud", "region", "tier", "storage_gib", "replicas", "estimated_price_per_hour_usd"} {
			for _, item := range list {
				if row, ok := item.(map[string]any); ok {
					if _, exists := row[key]; exists {
						keys = append(keys, key)
						break
					}
				}
			}
		}
		rows := make([]map[string]any, 0, len(list))
		for _, item := range list {
			if row, ok := item.(map[string]any); ok {
				rows = append(rows, row)
			} else {
				rows = append(rows, map[string]any{"value": item})
			}
		}
		return Table(w, rows, keys)
	}
	if obj, ok := value.(map[string]any); ok {
		return Table(w, []map[string]any{obj}, nil)
	}
	return Table(w, []map[string]any{{"value": value}}, nil)
}

// Table renders records horizontally with consistent headers and spacing.
// Empty columns selects all fields in sorted order.
func Table(w io.Writer, rows []map[string]any, columns []string) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "No results.")
		return err
	}
	if len(columns) == 0 {
		seen := map[string]bool{}
		for _, row := range rows {
			for key := range row {
				if !seen[key] {
					columns = append(columns, key)
					seen[key] = true
				}
			}
		}
		sort.Strings(columns)
	}
	visible := make([]string, 0, len(columns))
	for _, key := range columns {
		if hiddenTableColumn(key) {
			continue
		}
		visible = append(visible, key)
	}
	columns = visible
	if len(columns) == 0 {
		_, err := fmt.Fprintln(w, "No results.")
		return err
	}
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	headers := make([]string, len(columns))
	for i, key := range columns {
		headers[i] = strings.ToUpper(cell(key))
	}
	if _, err := fmt.Fprintln(table, strings.Join(headers, "\t")); err != nil {
		return err
	}
	for _, row := range rows {
		cells := make([]string, len(columns))
		for i, key := range columns {
			cells[i] = "-"
			if value, ok := row[key]; ok {
				cells[i] = cell(value)
			}
		}
		if _, err := fmt.Fprintln(table, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	return table.Flush()
}

func cell(v any) string {
	var s string
	if str, ok := v.(string); ok {
		s = str
	} else {
		b, _ := json.Marshal(v)
		s = string(b)
	}
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)
}

func redactDetails(value any) any {
	switch v := value.(type) {
	case map[string]any:
		delete(v, "can_write")
		delete(v, "nodes")
		if DeploymentTier(v) == "base" {
			delete(v, "enable_superuser")
		}
		if tier := DeploymentTier(v); tier != "" {
			v["tier"] = tier
			delete(v, "pricing_mode")
		}
		if min, minOK := v["min_replicas"].(float64); minOK {
			if max, maxOK := v["max_replicas"].(float64); maxOK {
				if min == max {
					v["replicas"] = max
				} else {
					v["replicas"] = fmt.Sprintf("%g–%g", min, max)
				}
				delete(v, "min_replicas")
				delete(v, "max_replicas")
			}
		}
		for key, entry := range v {
			if hiddenTableColumn(key) {
				delete(v, key)
				continue
			}
			lower := strings.ToLower(key)
			if strings.Contains(lower, "password") || strings.Contains(lower, "token") || strings.Contains(lower, "api_key") || strings.Contains(lower, "apikey") {
				v[key] = "[redacted; use --output json to retrieve]"
			} else {
				v[key] = redactDetails(entry)
			}
		}
	case []any:
		for i, entry := range v {
			v[i] = redactDetails(entry)
		}
	}
	return value
}

func hiddenTableColumn(key string) bool {
	key = strings.ToLower(key)
	switch key {
	case "status_message", "cluster_status", "ready_instances", "current_primary", "dedicated", "database_password", "vllm_api_key", "omnistrate_instance_id", "endpoint_url", "reader_endpoint_url", "writer_endpoint_url":
		return true
	}
	return strings.HasPrefix(key, "estimated_price_") && key != "estimated_price_per_hour_usd"
}

// DeploymentTier translates API tier fields into the labels used by the UI.
func DeploymentTier(v map[string]any) string {
	mode, _ := v["pricing_mode"].(string)
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "basic", "base":
		return "base"
	case "dedicated", "advanced", "scale":
		return "scale"
	case "enterprise":
		return "enterprise"
	}
	if strings.TrimSpace(mode) != "" {
		return ""
	}
	// Postgres Base responses hide all placement fields, including when
	// pricing_mode is absent. Require Postgres capacity fields as well.
	_, cpu := v["vcpu"]
	_, memory := v["memory_gib"]
	_, storage := v["storage_gib"]
	_, cloud := v["cloud"]
	_, region := v["region"]
	_, instance := v["instance_type"]
	if cpu && memory && storage && !cloud && !region && !instance {
		return "base"
	}
	if dedicated, ok := v["dedicated"].(bool); ok {
		if !dedicated {
			return "base"
		}
		return "scale"
	}
	return ""
}
