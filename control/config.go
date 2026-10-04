package main

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

const maxConfigRevisions = 20

type ConfigRevision struct {
	Revision  uint64        `json:"revision"`
	CreatedAt int64         `json:"created_at"`
	Source    string        `json:"source"`
	Config    GatewayConfig `json:"config"`
}

type ConfigRevisionSummary struct {
	Revision  uint64 `json:"revision"`
	CreatedAt int64  `json:"created_at"`
	Source    string `json:"source"`
}

func defaultGatewayConfig() GatewayConfig {
	upstreamURL := strings.TrimSpace(os.Getenv("HTTPV_DEFAULT_UPSTREAM_URL"))
	if upstreamURL == "" {
		upstreamURL = "http://host.docker.internal:9998"
	}
	hostHeader := strings.TrimSpace(os.Getenv("HTTPV_DEFAULT_UPSTREAM_HOST"))
	if hostHeader == "" {
		if parsed, err := url.Parse(upstreamURL); err == nil {
			hostHeader = parsed.Host
		}
	}
	return GatewayConfig{
		SchemaVersion: 1,
		Upstreams: []Upstream{{
			ID: "local-app", Name: "Local application", URL: upstreamURL, HostHeader: hostHeader,
			Targets:     []UpstreamTarget{{ID: "local-app-1", URL: upstreamURL, HostHeader: hostHeader, Weight: 1}},
			MaxFailures: 1, FailureTimeoutMS: 10_000,
		}},
		Routes: []Route{{ID: "default-route", Name: "Default route", Enabled: true, Match: Match{PathPrefix: "/"}, UpstreamID: "local-app"}},
		Subjects: []Subject{{
			ID: "default", Name: "Default traffic", Enabled: true, Display: true,
			Match: Match{PathPrefix: "/"}, Gate: Gate{Enabled: false, TimeoutMS: 30_000, TimeoutAction: "block", MaxPending: 2_000, OverflowAction: "block", FailureAction: "allow"},
			Capture: defaultCapturePolicy(),
		}},
		Rules: []Rule{},
	}
}

func defaultCapturePolicy() CapturePolicy {
	return CapturePolicy{
		RequestHeaders: true, ResponseHeaders: true, MaxBodyBytes: 16 * 1024, TTLSeconds: 60, SampleRate: 1,
		ContentTypes:  []string{"application/json", "application/x-www-form-urlencoded", "application/xml", "text/"},
		RedactHeaders: []string{"authorization", "cookie", "set-cookie", "proxy-authorization", "x-api-key"},
	}
}

func normalizeCapturePolicy(policy CapturePolicy) CapturePolicy {
	defaults := defaultCapturePolicy()
	if !policy.RequestHeaders && !policy.ResponseHeaders && !policy.RequestBody && !policy.ResponseBody {
		policy.RequestHeaders = true
		policy.ResponseHeaders = true
	}
	if policy.MaxBodyBytes <= 0 {
		policy.MaxBodyBytes = defaults.MaxBodyBytes
	}
	if policy.TTLSeconds <= 0 {
		policy.TTLSeconds = defaults.TTLSeconds
	}
	if policy.SampleRate <= 0 {
		policy.SampleRate = defaults.SampleRate
	}
	if len(policy.ContentTypes) == 0 {
		policy.ContentTypes = append([]string{}, defaults.ContentTypes...)
	}
	redacted := map[string]struct{}{}
	policy.RedactHeaders = append(policy.RedactHeaders, defaults.RedactHeaders...)
	normalizedHeaders := make([]string, 0, len(policy.RedactHeaders))
	for _, header := range policy.RedactHeaders {
		header = strings.ToLower(strings.TrimSpace(header))
		if header == "" {
			continue
		}
		if _, exists := redacted[header]; !exists {
			redacted[header] = struct{}{}
			normalizedHeaders = append(normalizedHeaders, header)
		}
	}
	policy.RedactHeaders = normalizedHeaders
	for index := range policy.ContentTypes {
		policy.ContentTypes[index] = strings.ToLower(strings.TrimSpace(policy.ContentTypes[index]))
	}
	return policy
}

func cloneGatewayConfig(config GatewayConfig) GatewayConfig {
	result := GatewayConfig{
		SchemaVersion: config.SchemaVersion,
		Upstreams:     append([]Upstream{}, config.Upstreams...),
		Routes:        append([]Route{}, config.Routes...),
		Subjects:      append([]Subject{}, config.Subjects...),
		Rules:         append([]Rule{}, config.Rules...),
	}
	for index := range result.Upstreams {
		result.Upstreams[index].Targets = append([]UpstreamTarget{}, config.Upstreams[index].Targets...)
	}
	for index := range result.Subjects {
		result.Subjects[index].Capture.ContentTypes = append([]string{}, config.Subjects[index].Capture.ContentTypes...)
		result.Subjects[index].Capture.RedactHeaders = append([]string{}, config.Subjects[index].Capture.RedactHeaders...)
	}
	return result
}

func normalizeMatch(match Match) Match {
	match.Host = strings.TrimSpace(match.Host)
	match.Method = strings.ToUpper(strings.TrimSpace(match.Method))
	match.PathPrefix = strings.TrimSpace(match.PathPrefix)
	if match.PathPrefix == "" {
		match.PathPrefix = "/"
	}
	return match
}

func normalizeGatewayConfig(config GatewayConfig) GatewayConfig {
	if config.SchemaVersion == 0 {
		config.SchemaVersion = 1
	}
	for index := range config.Upstreams {
		upstream := &config.Upstreams[index]
		upstream.ID = strings.TrimSpace(upstream.ID)
		upstream.Name = strings.TrimSpace(upstream.Name)
		upstream.URL = strings.TrimRight(strings.TrimSpace(upstream.URL), "/")
		upstream.HostHeader = strings.TrimSpace(upstream.HostHeader)
		if upstream.MaxFailures <= 0 {
			upstream.MaxFailures = 1
		}
		if upstream.FailureTimeoutMS <= 0 {
			upstream.FailureTimeoutMS = 10_000
		}
		if upstream.Name == "" {
			upstream.Name = upstream.ID
		}
		if upstream.HostHeader == "" {
			if parsed, err := url.Parse(upstream.URL); err == nil {
				upstream.HostHeader = parsed.Host
			}
		}
		if len(upstream.Targets) == 0 && upstream.URL != "" {
			upstream.Targets = []UpstreamTarget{{ID: upstream.ID + "-1", URL: upstream.URL, HostHeader: upstream.HostHeader, Weight: 1}}
		}
		for targetIndex := range upstream.Targets {
			target := &upstream.Targets[targetIndex]
			target.ID = strings.TrimSpace(target.ID)
			if target.ID == "" {
				target.ID = fmt.Sprintf("%s-%d", upstream.ID, targetIndex+1)
			}
			target.URL = strings.TrimRight(strings.TrimSpace(target.URL), "/")
			target.HostHeader = strings.TrimSpace(target.HostHeader)
			if target.HostHeader == "" {
				if parsed, err := url.Parse(target.URL); err == nil {
					target.HostHeader = parsed.Host
				}
			}
			if target.Weight <= 0 {
				target.Weight = 1
			}
		}
		if len(upstream.Targets) > 0 {
			upstream.URL = upstream.Targets[0].URL
			upstream.HostHeader = upstream.Targets[0].HostHeader
		}
	}
	for index := range config.Routes {
		route := &config.Routes[index]
		route.ID = strings.TrimSpace(route.ID)
		route.Name = strings.TrimSpace(route.Name)
		route.UpstreamID = strings.TrimSpace(route.UpstreamID)
		route.Match = normalizeMatch(route.Match)
		if route.Name == "" {
			route.Name = route.ID
		}
	}
	for index := range config.Subjects {
		subject := &config.Subjects[index]
		subject.ID = strings.TrimSpace(subject.ID)
		subject.Name = strings.TrimSpace(subject.Name)
		subject.Match = normalizeMatch(subject.Match)
		if subject.Name == "" {
			subject.Name = subject.ID
		}
		if subject.Gate.TimeoutAction != "allow" && subject.Gate.TimeoutAction != "block" {
			subject.Gate.TimeoutAction = "block"
		}
		if subject.Gate.OverflowAction != "allow" && subject.Gate.OverflowAction != "block" && subject.Gate.OverflowAction != "reject" {
			subject.Gate.OverflowAction = "block"
		}
		if subject.Gate.FailureAction != "allow" && subject.Gate.FailureAction != "block" {
			subject.Gate.FailureAction = "allow"
		}
		subject.Capture = normalizeCapturePolicy(subject.Capture)
	}
	for index := range config.Rules {
		rule := &config.Rules[index]
		rule.ID = strings.TrimSpace(rule.ID)
		if rule.ID == "" {
			rule.ID = fmt.Sprintf("rule-%d", index+1)
		}
		rule.SubjectID = strings.TrimSpace(rule.SubjectID)
		rule.Action = "block"
		rule.Method = strings.ToUpper(strings.TrimSpace(rule.Method))
		rule.PathPrefix = strings.TrimSpace(rule.PathPrefix)
		rule.ClientIP = strings.TrimSpace(rule.ClientIP)
	}
	return config
}

func validConfigID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return true
}

func validateGatewayConfig(config GatewayConfig) error {
	if config.SchemaVersion != 1 {
		return fmt.Errorf("unsupported configuration schema version %d", config.SchemaVersion)
	}
	if len(config.Upstreams) == 0 {
		return fmt.Errorf("at least one upstream is required")
	}
	upstreams := make(map[string]struct{}, len(config.Upstreams))
	for _, upstream := range config.Upstreams {
		if !validConfigID(upstream.ID) {
			return fmt.Errorf("invalid upstream id %q", upstream.ID)
		}
		if _, exists := upstreams[upstream.ID]; exists {
			return fmt.Errorf("duplicate upstream id %q", upstream.ID)
		}
		if len(upstream.Targets) == 0 {
			return fmt.Errorf("upstream %q requires at least one target", upstream.ID)
		}
		targets := map[string]struct{}{}
		for _, target := range upstream.Targets {
			if !validConfigID(target.ID) {
				return fmt.Errorf("upstream %q has invalid target id %q", upstream.ID, target.ID)
			}
			if _, exists := targets[target.ID]; exists {
				return fmt.Errorf("upstream %q has duplicate target id %q", upstream.ID, target.ID)
			}
			targets[target.ID] = struct{}{}
			parsed, err := url.Parse(target.URL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
				return fmt.Errorf("upstream %q target %q must use an http(s) origin URL without a path", upstream.ID, target.ID)
			}
			if target.Weight < 1 || target.Weight > 1000 {
				return fmt.Errorf("upstream %q target %q weight must be between 1 and 1000", upstream.ID, target.ID)
			}
		}
		if upstream.MaxFailures < 1 || upstream.MaxFailures > 100 {
			return fmt.Errorf("upstream %q max failures must be between 1 and 100", upstream.ID)
		}
		if upstream.FailureTimeoutMS < 100 || upstream.FailureTimeoutMS > 3_600_000 {
			return fmt.Errorf("upstream %q failure timeout must be between 100 and 3600000ms", upstream.ID)
		}
		upstreams[upstream.ID] = struct{}{}
	}
	if len(config.Routes) == 0 {
		return fmt.Errorf("at least one route is required")
	}
	routes := make(map[string]struct{}, len(config.Routes))
	for _, route := range config.Routes {
		if !validConfigID(route.ID) {
			return fmt.Errorf("invalid route id %q", route.ID)
		}
		if _, exists := routes[route.ID]; exists {
			return fmt.Errorf("duplicate route id %q", route.ID)
		}
		if _, exists := upstreams[route.UpstreamID]; !exists {
			return fmt.Errorf("route %q references unknown upstream %q", route.ID, route.UpstreamID)
		}
		if !strings.HasPrefix(route.Match.PathPrefix, "/") {
			return fmt.Errorf("route %q path prefix must start with /", route.ID)
		}
		routes[route.ID] = struct{}{}
	}
	subjects := make(map[string]struct{}, len(config.Subjects))
	for _, subject := range config.Subjects {
		if !validConfigID(subject.ID) {
			return fmt.Errorf("invalid subject id %q", subject.ID)
		}
		if _, exists := subjects[subject.ID]; exists {
			return fmt.Errorf("duplicate subject id %q", subject.ID)
		}
		if !strings.HasPrefix(subject.Match.PathPrefix, "/") {
			return fmt.Errorf("subject %q path prefix must start with /", subject.ID)
		}
		if subject.Gate.TimeoutMS < 0 {
			return fmt.Errorf("subject %q gate timeout cannot be negative", subject.ID)
		}
		if subject.Gate.MaxPending < 0 {
			return fmt.Errorf("subject %q maximum pending count cannot be negative", subject.ID)
		}
		if subject.Gate.TimeoutAction != "allow" && subject.Gate.TimeoutAction != "block" {
			return fmt.Errorf("subject %q has an invalid timeout action", subject.ID)
		}
		if subject.Gate.OverflowAction != "allow" && subject.Gate.OverflowAction != "block" && subject.Gate.OverflowAction != "reject" {
			return fmt.Errorf("subject %q has an invalid overflow action", subject.ID)
		}
		if subject.Gate.FailureAction != "allow" && subject.Gate.FailureAction != "block" {
			return fmt.Errorf("subject %q has an invalid failure action", subject.ID)
		}
		if subject.Capture.MaxBodyBytes < 1 || subject.Capture.MaxBodyBytes > 1<<20 {
			return fmt.Errorf("subject %q capture body limit must be between 1 and 1048576 bytes", subject.ID)
		}
		if subject.Capture.TTLSeconds < 1 || subject.Capture.TTLSeconds > 3600 {
			return fmt.Errorf("subject %q capture TTL must be between 1 and 3600 seconds", subject.ID)
		}
		if subject.Capture.SampleRate <= 0 || subject.Capture.SampleRate > 1 {
			return fmt.Errorf("subject %q capture sample rate must be greater than 0 and at most 1", subject.ID)
		}
		subjects[subject.ID] = struct{}{}
	}
	rules := make(map[string]struct{}, len(config.Rules))
	for _, rule := range config.Rules {
		if !validConfigID(rule.ID) {
			return fmt.Errorf("invalid rule id %q", rule.ID)
		}
		if _, exists := rules[rule.ID]; exists {
			return fmt.Errorf("duplicate rule id %q", rule.ID)
		}
		rules[rule.ID] = struct{}{}
		if rule.SubjectID != "" {
			if _, exists := subjects[rule.SubjectID]; !exists {
				return fmt.Errorf("rule %q references unknown subject %q", rule.ID, rule.SubjectID)
			}
		}
		if rule.PathPrefix == "" && rule.ClientIP == "" {
			return fmt.Errorf("rule %q requires a path prefix or client IP", rule.ID)
		}
	}
	return nil
}

func (s *Store) config() GatewayConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	config := cloneGatewayConfig(s.gateway)
	sort.Slice(config.Rules, func(i, j int) bool { return config.Rules[i].CreatedAt < config.Rules[j].CreatedAt })
	return config
}

func (s *Store) replaceConfig(config GatewayConfig) error {
	config = normalizeGatewayConfig(config)
	if err := validateGatewayConfig(config); err != nil {
		return err
	}
	s.mu.Lock()
	s.gateway = cloneGatewayConfig(config)
	s.mu.Unlock()
	return nil
}

func (s *Store) appendRevisionLocked(source string) uint64 {
	s.revision++
	revision := ConfigRevision{Revision: s.revision, CreatedAt: time.Now().UnixMilli(), Source: source, Config: cloneGatewayConfig(s.gateway)}
	s.revisions = append(s.revisions, revision)
	if len(s.revisions) > maxConfigRevisions {
		s.revisions = append([]ConfigRevision{}, s.revisions[len(s.revisions)-maxConfigRevisions:]...)
	}
	return s.revision
}

func (s *Store) resetRevisionHistory(source string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revision = 1
	s.revisions = []ConfigRevision{{Revision: 1, CreatedAt: time.Now().UnixMilli(), Source: source, Config: cloneGatewayConfig(s.gateway)}}
}

func (s *Store) applyConfig(config GatewayConfig, source string) (uint64, error) {
	config = normalizeGatewayConfig(config)
	if err := validateGatewayConfig(config); err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.gateway = cloneGatewayConfig(config)
	revision := s.appendRevisionLocked(source)
	s.mu.Unlock()
	return revision, nil
}

func (s *Store) configRevision() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revision
}

func (s *Store) revisionSummaries() []ConfigRevisionSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]ConfigRevisionSummary, 0, len(s.revisions))
	for index := len(s.revisions) - 1; index >= 0; index-- {
		revision := s.revisions[index]
		result = append(result, ConfigRevisionSummary{Revision: revision.Revision, CreatedAt: revision.CreatedAt, Source: revision.Source})
	}
	return result
}

func (s *Store) rollbackConfig(revision uint64) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, candidate := range s.revisions {
		if candidate.Revision == revision {
			s.gateway = cloneGatewayConfig(candidate.Config)
			return s.appendRevisionLocked(fmt.Sprintf("rollback:%d", revision)), nil
		}
	}
	return 0, fmt.Errorf("configuration revision %d was not found", revision)
}

func (s *Store) updateSubject(subject Subject) Subject {
	subject.Match = normalizeMatch(subject.Match)
	if subject.Name == "" {
		subject.Name = subject.ID
	}
	if subject.Gate.TimeoutAction != "allow" && subject.Gate.TimeoutAction != "block" {
		subject.Gate.TimeoutAction = "block"
	}
	if subject.Gate.OverflowAction != "allow" && subject.Gate.OverflowAction != "block" && subject.Gate.OverflowAction != "reject" {
		subject.Gate.OverflowAction = "block"
	}
	if subject.Gate.FailureAction != "allow" && subject.Gate.FailureAction != "block" {
		subject.Gate.FailureAction = "allow"
	}
	subject.Capture = normalizeCapturePolicy(subject.Capture)
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.gateway.Subjects {
		if s.gateway.Subjects[index].ID == subject.ID {
			s.gateway.Subjects[index] = subject
			s.appendRevisionLocked("subject:" + subject.ID)
			return subject
		}
	}
	s.gateway.Subjects = append(s.gateway.Subjects, subject)
	s.appendRevisionLocked("subject:" + subject.ID)
	return subject
}

func (s *Store) addRule(rule Rule) Rule {
	s.mu.Lock()
	defer s.mu.Unlock()
	rule.ID = "rule-" + time.Now().UTC().Format("20060102T150405.000000000")
	rule.Action = "block"
	rule.Method = strings.ToUpper(strings.TrimSpace(rule.Method))
	rule.CreatedAt = time.Now().UnixMilli()
	s.gateway.Rules = append(s.gateway.Rules, rule)
	s.appendRevisionLocked("rule:create")
	return rule
}

func (s *Store) removeRule(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.gateway.Rules {
		if s.gateway.Rules[index].ID == id {
			s.gateway.Rules = append(s.gateway.Rules[:index], s.gateway.Rules[index+1:]...)
			s.appendRevisionLocked("rule:delete")
			return true
		}
	}
	return false
}
