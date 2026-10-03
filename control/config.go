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
		Upstreams: []Upstream{{ID: "local-app", Name: "Local application", URL: upstreamURL, HostHeader: hostHeader}},
		Routes:    []Route{{ID: "default-route", Name: "Default route", Enabled: true, Match: Match{PathPrefix: "/"}, UpstreamID: "local-app"}},
		Subjects: []Subject{{
			ID: "default", Name: "Default traffic", Enabled: true, Display: true,
			Match: Match{PathPrefix: "/"}, Gate: Gate{Enabled: false, TimeoutMS: 30_000, TimeoutAction: "block", MaxPending: 2_000, OverflowAction: "block", FailureAction: "allow"},
		}},
		Rules: []Rule{},
	}
}

func cloneGatewayConfig(config GatewayConfig) GatewayConfig {
	return GatewayConfig{
		Upstreams: append([]Upstream{}, config.Upstreams...),
		Routes:    append([]Route{}, config.Routes...),
		Subjects:  append([]Subject{}, config.Subjects...),
		Rules:     append([]Rule{}, config.Rules...),
	}
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
	for index := range config.Upstreams {
		upstream := &config.Upstreams[index]
		upstream.ID = strings.TrimSpace(upstream.ID)
		upstream.Name = strings.TrimSpace(upstream.Name)
		upstream.URL = strings.TrimRight(strings.TrimSpace(upstream.URL), "/")
		upstream.HostHeader = strings.TrimSpace(upstream.HostHeader)
		if upstream.Name == "" {
			upstream.Name = upstream.ID
		}
		if upstream.HostHeader == "" {
			if parsed, err := url.Parse(upstream.URL); err == nil {
				upstream.HostHeader = parsed.Host
			}
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
		parsed, err := url.Parse(upstream.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("upstream %q must use an http(s) origin URL without a path", upstream.ID)
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
