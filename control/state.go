package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type persistedControlState struct {
	Version   int              `json:"version,omitempty"`
	Config    GatewayConfig    `json:"config,omitempty"`
	Revision  uint64           `json:"revision,omitempty"`
	Revisions []ConfigRevision `json:"revisions,omitempty"`
	// Subject and Rules keep backward compatibility with pre-product state files.
	Subject Subject `json:"subject"`
	Rules   []Rule  `json:"rules"`
}

func loadStore(path string, configPaths ...string) (*Store, error) {
	store := newStore()
	var configErr error
	if len(configPaths) > 0 && configPaths[0] != "" {
		data, err := os.ReadFile(configPaths[0])
		if err != nil {
			configErr = fmt.Errorf("read gateway config: %w", err)
		} else {
			var config GatewayConfig
			if err := json.Unmarshal(data, &config); err != nil {
				configErr = fmt.Errorf("decode gateway config: %w", err)
			} else if err := store.replaceConfig(config); err != nil {
				configErr = fmt.Errorf("validate gateway config: %w", err)
			} else {
				store.resetRevisionHistory("file")
			}
		}
	}
	if path == "" {
		return store, configErr
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return store, configErr
	}
	if err != nil {
		return nil, err
	}
	var state persistedControlState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if len(state.Config.Upstreams) > 0 || len(state.Config.Routes) > 0 {
		if err := store.replaceConfig(state.Config); err != nil {
			return nil, err
		}
		store.mu.Lock()
		if state.Revision > 0 {
			store.revision = state.Revision
		} else {
			store.revision = 1
		}
		store.revisions = append([]ConfigRevision{}, state.Revisions...)
		if len(store.revisions) == 0 {
			store.revisions = []ConfigRevision{{Revision: store.revision, CreatedAt: time.Now().UnixMilli(), Source: "recovered", Config: cloneGatewayConfig(store.gateway)}}
		}
		if len(store.revisions) > maxConfigRevisions {
			store.revisions = append([]ConfigRevision{}, store.revisions[len(store.revisions)-maxConfigRevisions:]...)
		}
		store.mu.Unlock()
		return store, nil
	}
	if state.Subject.ID != "" {
		state.Subject.Match = normalizeMatch(state.Subject.Match)
		store.gateway.Subjects = []Subject{state.Subject}
	}
	store.gateway.Rules = nil
	for _, rule := range state.Rules {
		if rule.ID != "" && rule.Action == "block" {
			store.gateway.Rules = append(store.gateway.Rules, rule)
		}
	}
	store.gateway = normalizeGatewayConfig(store.gateway)
	store.resetRevisionHistory("migration")
	return store, nil
}

func (s *Store) persist(path string) error {
	if path == "" {
		return nil
	}
	s.mu.RLock()
	state := persistedControlState{
		Version: 3, Config: cloneGatewayConfig(s.gateway), Revision: s.revision,
		Revisions: append([]ConfigRevision{}, s.revisions...),
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
