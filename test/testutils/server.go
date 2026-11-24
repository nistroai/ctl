package testutils

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// ValidateLiveServer binds a credentialed run to an explicitly approved API
// origin. Local HTTP fixtures construct their own CLI and do not use this gate.
func ValidateLiveServer(server, allowedHosts string) error {
	u, err := url.Parse(server)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		u.RawPath != "" || (u.Path != "" && u.Path != "/") || (u.Port() != "" && u.Port() != "443") {
		return errors.New("live API server must be a root HTTPS URL without credentials, query, or a nonstandard port")
	}
	validHost := regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)
	matched := false
	for _, entry := range strings.Split(allowedHosts, ",") {
		host := strings.ToLower(strings.TrimSpace(entry))
		if !validHost.MatchString(host) {
			return errors.New("live API host allowlist must contain explicit hostnames")
		}
		matched = matched || strings.EqualFold(host, u.Hostname())
	}
	if !matched {
		return errors.New("live API server is outside the configured exact-host allowlist")
	}
	return nil
}
