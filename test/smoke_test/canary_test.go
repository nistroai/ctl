package smoke_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"nistro-ctl/test/canaryutils"
	"nistro-ctl/test/testutils"
)

type canaryCase struct {
	product, tier, marker string
	create, update        map[string]any
}

type canaryTiming struct {
	lifecycle, cleanup, poll, probe time.Duration
}

// TestProductionCanary owns every deployment it mutates. Normal Go tests skip
// the live lifecycle; local fixtures below exercise the same implementation.
func TestProductionCanary(t *testing.T) {
	enabled := strings.TrimSpace(os.Getenv("ENABLE_CANARY_TEST"))
	if enabled == "" || enabled == "false" {
		t.Skip("set ENABLE_CANARY_TEST=true to provision lifecycle canaries")
	}
	if enabled != "true" {
		t.Fatal("ENABLE_CANARY_TEST must be true or false")
	}
	cases, err := canaryCaseNames(setting("NISTRO_CANARY_CASES", "postgres/on-demand,postgres/scale,inference/on-demand,inference/scale"))
	if err != nil {
		t.Fatal(err)
	}
	timing := canaryTiming{
		lifecycle: canaryDuration(t, "NISTRO_CANARY_TIMEOUT", 30*time.Minute),
		cleanup:   canaryDuration(t, "NISTRO_CANARY_CLEANUP_TIMEOUT", 5*time.Minute),
		poll:      canaryDuration(t, "NISTRO_CANARY_POLL_INTERVAL", 10*time.Second),
		probe:     canaryDuration(t, "NISTRO_CANARY_PROBE_TIMEOUT", 90*time.Second),
	}
	cli := newLiveCLI(t)
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			canary, err := prepareCanary(t, cli, name)
			if err != nil {
				t.Fatal(err)
			}
			if err := runCanaryLifecycle(t, cli, canary, timing); err != nil {
				t.Fatalf("%s canary failed: %v", name, err)
			}
			t.Logf("completed %s lifecycle with run marker %s", name, canary.marker)
		})
	}
}

func canaryCaseNames(value string) ([]string, error) {
	var cases []string
	seen := map[string]bool{}
	for _, value := range strings.Split(value, ",") {
		name := strings.TrimSpace(value)
		switch name {
		case "postgres/on-demand", "postgres/scale", "inference/on-demand", "inference/scale":
		default:
			return nil, errors.New("NISTRO_CANARY_CASES must select supported product/tier pairs")
		}
		if seen[name] {
			return nil, errors.New("NISTRO_CANARY_CASES must not contain duplicates")
		}
		seen[name] = true
		cases = append(cases, name)
	}
	return cases, nil
}

func canaryDuration(t *testing.T, key string, fallback time.Duration) time.Duration {
	t.Helper()
	value, err := time.ParseDuration(setting(key, fallback.String()))
	if err != nil || value <= 0 {
		t.Fatalf("%s must be a positive Go duration", key)
	}
	return value
}

func canaryMarker() (string, error) {
	var data [8]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", errors.New("generate unique canary marker")
	}
	return hex.EncodeToString(data[:]), nil
}

func prepareCanary(t *testing.T, cli *testutils.CLI, name string) (canaryCase, error) {
	product, tier, _ := strings.Cut(name, "/")
	marker, err := canaryMarker()
	if err != nil {
		return canaryCase{}, err
	}
	c := canaryCase{product: product, tier: tier, marker: marker, create: map[string]any{}}
	if product == "postgres" {
		var password [24]byte
		if _, err := rand.Read(password[:]); err != nil {
			return c, errors.New("generate canary database password")
		}
		c.create = map[string]any{
			"storage_gib": 50, "database_name": "nistro_canary_" + marker,
			"database_username": "canary_" + marker, "database_password": base64.RawURLEncoding.EncodeToString(password[:]),
		}
		if tier == "on-demand" {
			c.create["vcpu"], c.create["location"] = 2, "ANY"
		} else {
			c.create["replicas"] = 1
		}
	} else {
		c.create = map[string]any{"model_url": setting("NISTRO_CANARY_MODEL", "Qwen/Qwen2.5-0.5B-Instruct"), "max_model_len": 2048}
		if tier == "on-demand" {
			c.create["location"] = "ANY"
		} else {
			c.create["replicas"] = 1
		}
	}
	key := "NISTRO_CANARY_" + strings.ToUpper(strings.ReplaceAll(name, "/", "_"))
	key = strings.ReplaceAll(key, "-", "_")
	if err := mergeCanaryConfig(c.create, os.Getenv(key+"_CREATE_CONFIG")); err != nil {
		return c, fmt.Errorf("%s_CREATE_CONFIG must contain one JSON object", key)
	}
	if tier == "scale" && (canaryString(c.create, "cloud") == "" || canaryString(c.create, "region") == "" || canaryString(c.create, "instance_type") == "") {
		var cloud, region, instance string
		if product == "postgres" {
			cloud, region, instance, err = canaryPostgresPlacement(runJSON(t, cli, "postgres", "placements", "scale", "-o", "json"), c.create)
		} else {
			args := []string{"inference", "recommend-hardware", "scale", "--model-url", canaryString(c.create, "model_url"), "--max-model-len", fmt.Sprint(c.create["max_model_len"]), "--location", setting("NISTRO_CANARY_LOCATION", "US"), "-o", "json"}
			cloud, region, instance, err = canaryInferencePlacement(runJSON(t, cli, args...), c.create)
		}
		if err != nil {
			return c, err
		}
		c.create["cloud"], c.create["region"], c.create["instance_type"] = cloud, region, instance
	}
	if product == "inference" && tier == "on-demand" {
		if strings.TrimSpace(os.Getenv(key+"_UPDATE_CONFIG")) != "" {
			return c, errors.New("inference/on-demand has no CLI update command")
		}
		return c, nil
	}
	c.update = map[string]any{}
	if product == "postgres" && tier == "on-demand" {
		storage := canaryNumber(c.create["storage_gib"])
		if storage < 1024 {
			c.update["storage_gib"] = min(storage+10, 1024)
		} else {
			c.update["vcpu"] = min(canaryNumber(c.create["vcpu"])+1, 8)
		}
	} else {
		replicas := canaryNumber(c.create["replicas"])
		c.update["replicas"] = replicas + 1
		if replicas >= 8 {
			c.update["replicas"] = replicas - 1
		}
	}
	if value := strings.TrimSpace(os.Getenv(key + "_UPDATE_CONFIG")); value != "" {
		c.update = map[string]any{}
		if err := mergeCanaryConfig(c.update, value); err != nil {
			return c, fmt.Errorf("%s_UPDATE_CONFIG must contain one JSON object", key)
		}
	}
	changed := false
	for key, value := range c.update {
		changed = changed || !canaryEqual(value, c.create[key])
	}
	if !changed {
		return c, errors.New("canary update must change at least one create setting")
	}
	return c, nil
}

func mergeCanaryConfig(config map[string]any, value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	if len(value) > 1<<20 {
		return errors.New("configuration exceeds 1 MiB")
	}
	var supplied map[string]any
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	if decoder.Decode(&supplied) != nil || supplied == nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("expected one JSON object")
	}
	for key, value := range supplied {
		config[key] = value
	}
	return nil
}

func canaryPostgresPlacement(catalog, preferred map[string]any) (string, string, string, error) {
	clouds, _ := catalog["clouds"].([]any)
	for _, rawCloud := range clouds {
		cloud, _ := rawCloud.(map[string]any)
		regions, _ := cloud["regions"].([]any)
		for _, rawRegion := range regions {
			region, _ := rawRegion.(map[string]any)
			instances, _ := region["instance_types"].([]any)
			for _, rawInstance := range instances {
				instance, _ := rawInstance.(string)
				if canaryPlacementMatches(preferred, canaryString(cloud, "cloud"), canaryString(region, "region"), instance) {
					return canaryString(cloud, "cloud"), canaryString(region, "region"), instance, nil
				}
			}
		}
	}
	return "", "", "", errors.New("no usable Postgres catalog placement matches canary configuration")
}

func canaryInferencePlacement(response, preferred map[string]any) (string, string, string, error) {
	options, _ := response["deployment_options"].([]any)
	for _, raw := range options {
		offer, _ := raw.(map[string]any)
		cloud, region, sku := canaryString(offer, "cloud"), canaryString(offer, "region"), canaryString(offer, "sku")
		if canaryPlacementMatches(preferred, cloud, region, sku) {
			return cloud, region, sku, nil
		}
	}
	return "", "", "", errors.New("no usable inference recommendation matches canary configuration")
}

func canaryPlacementMatches(preferred map[string]any, cloud, region, instance string) bool {
	if cloud == "" || region == "" || instance == "" {
		return false
	}
	for key, actual := range map[string]string{"cloud": cloud, "region": region, "instance_type": instance} {
		if want := canaryString(preferred, key); want != "" && want != actual {
			return false
		}
	}
	return true
}

func runCanaryLifecycle(t *testing.T, cli *testutils.CLI, c canaryCase, timing canaryTiming) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timing.lifecycle)
	defer cancel()
	baseline, err := canaryCommand(ctx, cli, c.product, "list", "-o", "json")
	if err != nil {
		return fmt.Errorf("baseline list: %w", err)
	}
	priorIDs, err := canaryListIDs(baseline)
	if err != nil {
		return err
	}
	createPath, err := canaryConfigFile(t, c.create)
	if err != nil {
		return err
	}
	created, createError := canaryCommand(ctx, cli, c.product, "create", c.tier, "--config-file", createPath, "-o", "json")
	if createError != nil && created == nil {
		return fmt.Errorf("create: %w", createError)
	}
	resource := canaryResource(created)
	id := canaryString(resource, "id")
	if !validCanaryID(id) {
		return errors.New("create did not return a valid owned deployment ID; check the create operation before retrying")
	}
	if priorIDs[id] {
		return errors.New("create returned a preexisting deployment ID; refusing to mutate or delete it")
	}
	deleted := false
	t.Cleanup(func() {
		if deleted {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), timing.cleanup)
		defer cancel()
		if err := deleteCanary(cleanupCtx, cli, c.product, id, timing.poll); err != nil {
			t.Errorf("cleanup of owned %s canary %s failed: %v", c.product, id, err)
		}
	})
	t.Logf("created owned %s/%s canary %s (run marker %s)", c.product, c.tier, id, c.marker)
	if createError != nil {
		// Even a failed CLI invocation can have emitted an owned ID after the
		// server accepted creation. Register cleanup before reporting failure.
		return fmt.Errorf("create: %w", createError)
	}
	if err := waitCanaryListed(ctx, cli, c.product, id, timing.poll); err != nil {
		return fmt.Errorf("list created deployment: %w", err)
	}
	probe := &canaryProbe{caseInfo: c, marker: c.marker, cli: cli}
	if _, err := waitCanaryReady(ctx, cli, c.product, id, nil, probe, timing); err != nil {
		return fmt.Errorf("initial readiness/data plane: %w", err)
	}
	if c.update != nil {
		updatePath, err := canaryConfigFile(t, c.update)
		if err != nil {
			return err
		}
		updated, err := canaryCommand(ctx, cli, c.product, "update", c.tier, id, "--config-file", updatePath, "-o", "json")
		if err != nil {
			return fmt.Errorf("update: %w", err)
		}
		if returnedID := canaryString(canaryResource(updated), "id"); returnedID != "" && returnedID != id {
			return errors.New("update returned a different deployment ID")
		}
		probe.afterUpdate = true
		if _, err := waitCanaryReady(ctx, cli, c.product, id, c.update, probe, timing); err != nil {
			return fmt.Errorf("updated readiness/data plane: %w", err)
		}
	}
	if probe.postgres != nil {
		probeCtx, cancel := context.WithTimeout(ctx, timing.probe)
		err := probe.postgres.Cleanup(probeCtx, probe.connectionString)
		cancel()
		if err != nil {
			return errors.New("remove SQL checkpoint before deletion")
		}
	}
	if err := deleteCanary(ctx, cli, c.product, id, timing.poll); err != nil {
		return fmt.Errorf("delete/confirmation: %w", err)
	}
	deleted = true
	return nil
}

func waitCanaryListed(ctx context.Context, cli *testutils.CLI, product, id string, interval time.Duration) error {
	for {
		response, err := canaryCommand(ctx, cli, product, "list", "-o", "json")
		if err == nil {
			ids, err := canaryListIDs(response)
			if err != nil {
				return err
			}
			if ids[id] {
				return nil
			}
		} else if !canaryRetryable(err, false) {
			return err
		}
		if err := canaryPause(ctx, interval); err != nil {
			return err
		}
	}
}

func canaryConfigFile(t *testing.T, config map[string]any) (string, error) {
	data, err := json.Marshal(config)
	if err != nil {
		return "", errors.New("encode canary configuration")
	}
	path := filepath.Join(t.TempDir(), "canary.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", errors.New("save canary configuration")
	}
	return path, nil
}

type canaryCommandFailure struct {
	status int
	text   string
}

func (e *canaryCommandFailure) Error() string { return e.text }

var canaryHTTPStatus = regexp.MustCompile(`server returned ([0-9]{3})`)

func canaryCommand(ctx context.Context, cli *testutils.CLI, args ...string) (map[string]any, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	result := cli.RunContext(ctx, "", args...)
	var object map[string]any
	decoder := json.NewDecoder(strings.NewReader(result.Stdout))
	decoder.UseNumber()
	validObject := decoder.Decode(&object) == nil && object != nil && decoder.Decode(new(any)) == io.EOF
	if !validObject {
		object = nil
	}
	if result.Err != nil || result.ExitCode != 0 {
		if ctx.Err() != nil {
			return object, ctx.Err()
		}
		status := 0
		if match := canaryHTTPStatus.FindStringSubmatch(result.Stderr); len(match) == 2 {
			_, _ = fmt.Sscanf(match[1], "%d", &status)
		}
		return object, &canaryCommandFailure{status: status, text: fmt.Sprintf("CLI command failed (HTTP status %d; exit %d)", status, result.ExitCode)}
	}
	if result.Stderr != "" || !validObject {
		return object, errors.New("CLI command did not return one clean JSON object")
	}
	return object, nil
}

func canaryRetryable(err error, allowNotFound bool) bool {
	var failure *canaryCommandFailure
	if !errors.As(err, &failure) {
		return false
	}
	return failure.status == 429 || failure.status >= 500 || allowNotFound && failure.status == 404
}

func canaryResource(response map[string]any) map[string]any {
	if deployment, ok := response["deployment"].(map[string]any); ok {
		return deployment
	}
	return response
}

func validCanaryID(id string) bool {
	return strings.TrimSpace(id) != "" && id != "." && id != ".." && !strings.ContainsAny(id, "/\\?#")
}

func canaryListIDs(response map[string]any) (map[string]bool, error) {
	rows, ok := response["deployments"].([]any)
	if !ok {
		return nil, errors.New("CLI list did not return a deployments array")
	}
	ids := map[string]bool{}
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		id := canaryString(row, "id")
		if !validCanaryID(id) {
			return nil, errors.New("CLI list contained an invalid deployment ID")
		}
		ids[id] = true
	}
	return ids, nil
}

func waitCanaryReady(ctx context.Context, cli *testutils.CLI, product, id string, target map[string]any, probe *canaryProbe, timing canaryTiming) (map[string]any, error) {
	observedUpdate := target == nil
	var lastProbeError error
	deadlineError := func(err error) error {
		if !observedUpdate {
			return errors.New("actual desired update values were not observed before the deadline")
		}
		return errors.Join(err, lastProbeError)
	}
	for {
		if ctx.Err() != nil {
			return nil, deadlineError(ctx.Err())
		}
		response, err := canaryCommand(ctx, cli, product, "describe", id, "-o", "json")
		if err != nil {
			if ctx.Err() != nil {
				return nil, deadlineError(ctx.Err())
			}
			if !canaryRetryable(err, true) {
				return nil, err
			}
		} else {
			resource := canaryResource(response)
			if canaryString(resource, "id") != id {
				return nil, errors.New("describe returned a different deployment ID")
			}
			status := canaryString(resource, "status")
			if canaryTerminal(status) {
				return nil, errors.New("deployment entered a terminal failure state")
			}
			if canaryReady(status) && canaryUpdateObserved(resource, product, target) {
				observedUpdate = true
				probeCtx, cancel := context.WithTimeout(ctx, timing.probe)
				err = probe.check(probeCtx, response)
				cancel()
				if err == nil {
					return resource, nil
				}
				lastProbeError = err
			}
		}
		if err := canaryPause(ctx, timing.poll); err != nil {
			return nil, deadlineError(err)
		}
	}
}

func canaryReady(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "ready", "active", "available", "healthy", "deployed", "completed", "complete", "succeeded", "successful", "idle", "scaled to zero", "scaled_to_zero":
		return true
	}
	return false
}

func canaryTerminal(status string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	return strings.Contains(status, "failed") || status == "error" || status == "deleted" || status == "deleting" || status == "terminated" || status == "cancelled" || status == "canceled" || status == "stopped"
}

func canaryUpdateObserved(resource map[string]any, product string, target map[string]any) bool {
	if target == nil {
		return true
	}
	for key, want := range target {
		if key == "parameters" && product == "postgres" {
			// Nonempty parameter updates are proven against current_setting on
			// the live database by the post-update probe, rather than comparing
			// a public configuration map that may contain additional defaults.
			if settings, ok := want.(map[string]any); ok && len(settings) > 0 {
				continue
			}
			if settings, ok := want.(map[string]string); ok && len(settings) > 0 {
				continue
			}
		}
		if key == "replicas" && product == "inference" {
			minimum, minOK := resource["min_replicas"]
			maximum, maxOK := resource["max_replicas"]
			if minOK && maxOK {
				if !canaryEqual(minimum, want) || !canaryEqual(maximum, want) {
					return false
				}
				continue
			}
		}
		actual, exists := resource[key]
		if !exists {
			return false
		} else if !canaryEqual(actual, want) {
			return false
		}
	}
	return true
}

func deleteCanary(ctx context.Context, cli *testutils.CLI, product, id string, interval time.Duration) error {
	for {
		_, err := canaryCommand(ctx, cli, product, "delete", id, "-o", "json")
		var failure *canaryCommandFailure
		if err == nil || errors.As(err, &failure) && failure.status == 404 {
			break
		}
		if !canaryRetryable(err, false) {
			return err
		}
		if err := canaryPause(ctx, interval); err != nil {
			return err
		}
	}
	for {
		details, err := canaryCommand(ctx, cli, product, "describe", id, "-o", "json")
		var failure *canaryCommandFailure
		gone := errors.As(err, &failure) && failure.status == 404
		if err == nil {
			resource := canaryResource(details)
			if canaryString(resource, "id") != id {
				return errors.New("delete confirmation returned a different deployment ID")
			}
			status := strings.ToLower(canaryString(resource, "status"))
			gone = status == "deleted" || status == "terminated"
		} else if !gone && !canaryRetryable(err, false) {
			return err
		}
		if gone {
			listed, err := canaryCommand(ctx, cli, product, "list", "-o", "json")
			if err != nil && !canaryRetryable(err, false) {
				return err
			}
			if err == nil {
				ids, err := canaryListIDs(listed)
				if err != nil {
					return err
				}
				if !ids[id] {
					return nil
				}
			}
		}
		if err := canaryPause(ctx, interval); err != nil {
			return err
		}
	}
}

func canaryPause(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func canaryString(object map[string]any, field string) string {
	value, _ := object[field].(string)
	return strings.TrimSpace(value)
}

func canaryNumber(value any) float64 {
	switch value := value.(type) {
	case json.Number:
		number, _ := value.Float64()
		return number
	case float64:
		return value
	case int:
		return float64(value)
	}
	return 0
}

func canaryEqual(a, b any) bool {
	// JSON numeric values such as 2 and 2.0 express the same target. Do not
	// coerce string or boolean fields into numbers when comparing responses.
	switch a.(type) {
	case json.Number, float64, int:
		switch b.(type) {
		case json.Number, float64, int:
			return canaryNumber(a) == canaryNumber(b)
		}
	}
	first, err := json.Marshal(a)
	if err != nil {
		return false
	}
	second, err := json.Marshal(b)
	return err == nil && string(first) == string(second)
}

type canaryProbe struct {
	caseInfo         canaryCase
	marker           string
	cli              *testutils.CLI
	postgres         *canaryutils.PostgresCheckpoint
	connectionString string
	afterUpdate      bool
}

func (p *canaryProbe) check(ctx context.Context, response map[string]any) error {
	resource := canaryResource(response)
	if p.caseInfo.product == "inference" {
		endpoint := canaryString(resource, "endpoint_url")
		key := canaryString(resource, "vllm_api_key")
		if key == "" {
			key = canaryString(resource, "api_key")
		}
		if endpoint == "" || key == "" {
			return errors.New("inference endpoint and deployment key are not available yet")
		}
		p.cli.AddSecrets(key)
		expected := strings.TrimPrefix(canaryString(p.caseInfo.create, "model_url"), "https://huggingface.co/")
		reported := canaryString(resource, "model_url")
		if reported == "" {
			reported = canaryString(resource, "model")
		}
		if reported != "" && strings.TrimPrefix(reported, "https://huggingface.co/") != expected {
			return errors.New("inference deployment does not report the configured model")
		}
		if servedModel := strings.TrimSpace(os.Getenv("NISTRO_CANARY_SERVED_MODEL")); servedModel != "" {
			return canaryutils.CheckInferenceExact(ctx, endpoint, key, servedModel)
		}
		return canaryutils.CheckInference(ctx, endpoint, key, expected)
	}
	connectionString, err := canaryPostgresConnection(response, p.caseInfo.create)
	if err != nil {
		return err
	}
	p.connectionString = connectionString
	p.cli.AddSecrets(connectionString, canaryString(p.caseInfo.create, "database_password"))
	if p.postgres == nil {
		checkpoint, err := canaryutils.NewPostgresCheckpoint(ctx, connectionString, p.marker)
		if err != nil {
			return err
		}
		p.postgres = checkpoint
		return nil
	}
	if err := p.postgres.Verify(ctx, connectionString); err != nil {
		return err
	}
	if p.afterUpdate {
		settings := map[string]string{}
		if parameters, ok := p.caseInfo.update["parameters"].(map[string]any); ok {
			for key, value := range parameters {
				text, ok := value.(string)
				if !ok {
					return errors.New("Postgres update settings must be strings")
				}
				settings[key] = text
			}
		}
		if parameters, ok := p.caseInfo.update["parameters"].(map[string]string); ok {
			settings = parameters
		}
		if len(settings) > 0 {
			return canaryutils.CheckPostgresSettings(ctx, connectionString, settings)
		}
	}
	return nil
}

func canaryPostgresConnection(response, create map[string]any) (string, error) {
	resource := canaryResource(response)
	endpoint := canaryString(resource, "connection_string")
	if endpoint == "" {
		endpoint = canaryString(resource, "writer_endpoint_url")
	}
	if endpoint == "" {
		return "", errors.New("Postgres writer endpoint is not available yet")
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "postgres://" + endpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Hostname() == "" {
		return "", errors.New("Postgres writer endpoint has no usable host")
	}
	if parsed.Scheme == "http" || parsed.Scheme == "https" {
		// Older APIs represented every network endpoint as an HTTPS URL. The
		// named writer host is authoritative, with documented Postgres port
		// 5432. Other topology endpoints may refer to readers or observability.
		parsed = &url.URL{Scheme: "postgres", Host: net.JoinHostPort(parsed.Hostname(), "5432")}
	} else if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return "", errors.New("Postgres writer endpoint has an unsupported protocol")
	}
	if parsed.Port() == "" {
		parsed.Host = net.JoinHostPort(parsed.Hostname(), "5432")
	}
	username := canaryString(resource, "database_username")
	if username == "" {
		username = canaryString(create, "database_username")
	}
	password := canaryString(resource, "database_password")
	if password == "" {
		password = canaryString(create, "database_password")
	}
	database := canaryString(resource, "database_name")
	if database == "" {
		database = canaryString(create, "database_name")
	}
	if username == "" || password == "" || database == "" {
		return "", errors.New("Postgres connection credentials are incomplete")
	}
	parsed.User, parsed.Path = url.UserPassword(username, password), "/"+database
	query := parsed.Query()
	query.Set("sslmode", "verify-full")
	if rootCert := strings.TrimSpace(os.Getenv("NISTRO_CANARY_POSTGRES_SSLROOTCERT")); rootCert != "" {
		query.Set("sslrootcert", rootCert)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
