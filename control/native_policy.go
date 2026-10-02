package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	nativePolicyVersion  = 1
	nativePolicyMaxRules = 32
)

// NativePolicyPublisher writes the small, common-case policy subset consumed
// by the HTTPV dynamic module. It is intentionally separate from the richer
// Lua policy representation: gates and unsupported rule shapes stay on the
// Lua compatibility path.
type NativePolicyPublisher struct {
	path string
	mu   sync.Mutex
}

func newNativePolicyPublisher(path string) *NativePolicyPublisher {
	return &NativePolicyPublisher{path: path}
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
	buffer := []byte{'H', 'T', 'V', 'C', nativePolicyVersion, 0, 0, 0, 0, 0}
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

func (p *NativePolicyPublisher) publish(config GatewayConfig) error {
	if p == nil || p.path == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	buffer, err := encodeNativePolicy(config)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o777); err != nil {
		return err
	}
	temporary := fmt.Sprintf("%s.%d.tmp", p.path, os.Getpid())
	if err := os.WriteFile(temporary, buffer, 0o644); err != nil {
		return err
	}
	if err := os.Rename(temporary, p.path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}
