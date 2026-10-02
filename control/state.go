package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type persistedControlState struct {
	Subject Subject `json:"subject"`
	Rules   []Rule  `json:"rules"`
}

func loadStore(path string) (*Store, error) {
	store := newStore()
	if path == "" {
		return store, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	var state persistedControlState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state.Subject.ID != "" {
		store.updateSubject(state.Subject)
	}
	for _, rule := range state.Rules {
		if rule.ID != "" && rule.Action == "block" {
			store.rules[rule.ID] = rule
		}
	}
	return store, nil
}

func (s *Store) persist(path string) error {
	if path == "" {
		return nil
	}
	s.mu.RLock()
	state := persistedControlState{Subject: s.subject, Rules: make([]Rule, 0, len(s.rules))}
	for _, rule := range s.rules {
		state.Rules = append(state.Rules, rule)
	}
	s.mu.RUnlock()
	buffer, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	temporary := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(temporary, buffer, 0o640); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}
