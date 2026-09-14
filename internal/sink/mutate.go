package sink

import (
	"fmt"
	"strings"
)

// GetSink returns one sink by id from the live config.
func (m *Manager) GetSink(id string) (SinkSpec, bool) {
	id = strings.TrimSpace(id)
	if m == nil || id == "" {
		return SinkSpec{}, false
	}
	cfg := m.ConfigSnapshot()
	for _, s := range cfg.Sinks {
		if s.ID == id {
			return s, true
		}
	}
	return SinkSpec{}, false
}

// UpsertSink inserts or replaces a sink by id and persists sinks.json.
func (m *Manager) UpsertSink(spec SinkSpec) (SinkSpec, error) {
	if m == nil {
		return SinkSpec{}, fmt.Errorf("sinks unavailable")
	}
	spec.normalize()
	if spec.ID == "" {
		return SinkSpec{}, fmt.Errorf("id required")
	}
	if !sinkIDRe.MatchString(spec.ID) {
		return SinkSpec{}, fmt.Errorf("id %q must match %s", spec.ID, sinkIDRe.String())
	}
	m.mu.Lock()
	cfg := cloneConfig(m.cfg)
	replaced := false
	for i := range cfg.Sinks {
		if cfg.Sinks[i].ID == spec.ID {
			cfg.Sinks[i] = spec
			replaced = true
			break
		}
	}
	if !replaced {
		cfg.Sinks = append(cfg.Sinks, spec)
	}
	cfg.Normalize()
	var out SinkSpec
	for i := range cfg.Sinks {
		if cfg.Sinks[i].ID == spec.ID {
			out = cfg.Sinks[i]
			break
		}
	}
	m.cfg = cfg
	path := m.path
	m.mu.Unlock()
	if path != "" {
		if err := Save(path, cfg); err != nil {
			return out, err
		}
	}
	return out, nil
}

// DeleteSink removes a sink by id and persists sinks.json.
func (m *Manager) DeleteSink(id string) error {
	if m == nil {
		return fmt.Errorf("sinks unavailable")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("id required")
	}
	m.mu.Lock()
	cfg := cloneConfig(m.cfg)
	next := make([]SinkSpec, 0, len(cfg.Sinks))
	found := false
	for _, s := range cfg.Sinks {
		if s.ID == id {
			found = true
			continue
		}
		next = append(next, s)
	}
	if !found {
		m.mu.Unlock()
		return fmt.Errorf("sink %q not found", id)
	}
	cfg.Sinks = next
	m.cfg = cfg
	path := m.path
	m.mu.Unlock()
	if path != "" {
		return Save(path, cfg)
	}
	return nil
}

// SetDeepLinkBase updates the global deep_link_base and persists.
func (m *Manager) SetDeepLinkBase(base string) error {
	if m == nil {
		return fmt.Errorf("sinks unavailable")
	}
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	m.mu.Lock()
	cfg := cloneConfig(m.cfg)
	cfg.DeepLinkBase = base
	m.cfg = cfg
	path := m.path
	m.mu.Unlock()
	if path != "" {
		return Save(path, cfg)
	}
	return nil
}
