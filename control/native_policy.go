package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	nativePolicyVersion   = 1
	nativePolicyHeaderLen = 18
	nativePolicyMaxRules  = 32
)

// NativePolicyPublisher writes the small, common-case policy subset consumed
// by the HTTPV dynamic module. It is intentionally separate from the richer
// Lua policy representation: gates and unsupported rule shapes stay on the
// Lua compatibility path.
type NativePolicyPublisher struct {
	path        string
	versionPath string
	mu          sync.Mutex
	version     uint64
	fingerprint string
}

type NativePolicyAck struct {
	Worker  string `json:"worker"`
	Version uint64 `json:"version"`
}

type NativePolicyStatus struct {
	Version uint64            `json:"version"`
	State   string            `json:"state"`
	Acks    []NativePolicyAck `json:"acks"`
}

func newNativePolicyPublisher(path string) *NativePolicyPublisher {
	publisher := &NativePolicyPublisher{path: path}
	if path == "" {
		return publisher
	}
	publisher.versionPath = path + ".version"
	if data, err := os.ReadFile(publisher.versionPath); err == nil {
		if version, parseErr := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); parseErr == nil {
			publisher.version = version
		}
	}
	return publisher
}

func appendNativePolicyText(buffer []byte, value string) ([]byte, error) {
	if len(value) > 65_535 {
		return nil, fmt.Errorf("native policy value is too long")
	}
	length := make([]byte, 2)
	binary.BigEndian.PutUint16(length, uint16(len(value)))
	buffer = append(buffer, length...)
	return append(buffer, value...), nil
}

func encodeNativePolicy(config GatewayConfig) ([]byte, error) {
	return encodeNativePolicyVersion(config, 1)
}

func encodeNativePolicyVersion(config GatewayConfig, version uint64) ([]byte, error) {
	var subject Subject
	if len(config.Subjects) > 0 {
		subject = config.Subjects[0]
	}
	rules := make([]Rule, 0, nativePolicyMaxRules)
	for _, rule := range config.Rules {
		if rule.Action == "block" && len(rules) < nativePolicyMaxRules {
			rules = append(rules, rule)
		}
	}
	buffer := make([]byte, nativePolicyHeaderLen)
	copy(buffer, []byte{'H', 'T', 'V', 'C', nativePolicyVersion})
	if subject.Enabled {
		buffer[5] = 1
	}
	if subject.Display {
		buffer[6] = 1
	}
	if subject.Gate.Enabled {
		buffer[7] = 1
	}
	binary.BigEndian.PutUint16(buffer[8:10], uint16(len(rules)))
	binary.BigEndian.PutUint64(buffer[10:18], version)
	var err error
	for _, value := range []string{subject.ID, subject.Match.Host, subject.Match.PathPrefix, subject.Match.Method} {
		buffer, err = appendNativePolicyText(buffer, value)
		if err != nil {
			return nil, err
		}
	}
	for _, rule := range rules {
		for _, value := range []string{rule.Method, rule.PathPrefix, rule.ClientIP} {
			buffer, err = appendNativePolicyText(buffer, value)
			if err != nil {
				return nil, err
			}
		}
	}
	return buffer, nil
}

func (p *NativePolicyPublisher) publish(config GatewayConfig) (uint64, error) {
	if p == nil || p.path == "" {
		return 0, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	fingerprint, err := json.Marshal(config)
	if err != nil {
		return 0, err
	}
	if p.version > 0 && p.fingerprint == string(fingerprint) {
		return p.version, nil
	}
	nextVersion := p.version + 1
	buffer, err := encodeNativePolicyVersion(config, nextVersion)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o777); err != nil {
		return 0, err
	}
	temporary := fmt.Sprintf("%s.%d.tmp", p.path, os.Getpid())
	if err := os.WriteFile(temporary, buffer, 0o644); err != nil {
		return 0, err
	}
	if err := os.Rename(temporary, p.path); err != nil {
		_ = os.Remove(temporary)
		return 0, err
	}
	versionFile := fmt.Sprintf("%s.%d.tmp", p.versionPath, os.Getpid())
	if err := os.WriteFile(versionFile, []byte(strconv.FormatUint(nextVersion, 10)+"\n"), 0o640); err != nil {
		return 0, err
	}
	if err := os.Rename(versionFile, p.versionPath); err != nil {
		_ = os.Remove(versionFile)
		return 0, err
	}
	p.version = nextVersion
	p.fingerprint = string(fingerprint)
	return p.version, nil
}

func (p *NativePolicyPublisher) status() NativePolicyStatus {
	if p == nil || p.path == "" {
		return NativePolicyStatus{State: "disabled"}
	}
	p.mu.Lock()
	version := p.version
	path := p.path
	p.mu.Unlock()
	status := NativePolicyStatus{Version: version, State: "unpublished"}
	if version == 0 {
		return status
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		status.State = "pending"
		return status
	}
	prefix := filepath.Base(path) + ".ack."
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		content, err := os.ReadFile(filepath.Join(filepath.Dir(path), entry.Name()))
		if err != nil {
			continue
		}
		ackVersion, err := strconv.ParseUint(strings.TrimSpace(string(content)), 10, 64)
		if err == nil {
			status.Acks = append(status.Acks, NativePolicyAck{Worker: strings.TrimPrefix(entry.Name(), prefix), Version: ackVersion})
		}
	}
	sort.Slice(status.Acks, func(i, j int) bool { return status.Acks[i].Worker < status.Acks[j].Worker })
	status.State = "pending"
	if len(status.Acks) > 0 {
		allCurrent := true
		for _, ack := range status.Acks {
			if ack.Version != version {
				allCurrent = false
				break
			}
		}
		if allCurrent {
			status.State = "active"
		}
	}
	return status
}
