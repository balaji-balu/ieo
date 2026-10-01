package mockcontainerd

import (
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/balaji-balu/ieo/internal/en/plugins"
	"github.com/balaji-balu/ieo/pkg/en/edgeruntime"
	"github.com/balaji-balu/ieo/pkg/logx"
)

func init() {
	plugins.Register(&MockContainerd{})
}

// mockState is the in-memory lifecycle state of a mock component.
type mockState string

// Mock component states.
const (
	StateNone    mockState = "None"
	StatePulled  mockState = "Pulled"
	StateStarted mockState = "Started"
)

type container struct {
	spec  edgeruntime.ComponentSpec
	state mockState
}

// MockContainerd is an in-memory runtime plugin that simulates containerd for development and tests.
type MockContainerd struct {
	mu     sync.Mutex
	items  map[string]*container
	logger *zap.SugaredLogger
}

// Name returns the plugin name used to select it in the registry.
func (m *MockContainerd) Name() string {
	return "mock-containerd"
}

// Capabilities lists the workload types this plugin claims to run.
func (m *MockContainerd) Capabilities() []string {
	return []string{"oci", "mock"}
}

func (m *MockContainerd) ensure() {
	if m.items == nil {
		m.items = map[string]*container{}
	}
	if m.logger == nil {
		m.logger = logx.New("en.mockcontainerd")
	}
}

// Install records the component as pulled without pulling an image.
func (m *MockContainerd) Install(spec edgeruntime.ComponentSpec) error {
	m.ensure()
	m.mu.Lock()
	defer m.mu.Unlock()

	if spec.Artifact == "" {
		return fmt.Errorf("artifact is empty")
	}

	m.logger.Infow("Mock Install", "name", spec.Name, "artifact", spec.Artifact)

	m.items[spec.Name] = &container{
		spec:  spec,
		state: StatePulled,
	}
	return nil
}

// Start marks the component as started.
func (m *MockContainerd) Start(spec edgeruntime.ComponentSpec) error {
	m.ensure()
	m.mu.Lock()
	defer m.mu.Unlock()

	m.logger.Infow("Mock Start", "name", spec.Name)

	item, ok := m.items[spec.Name]
	if !ok {
		return fmt.Errorf("mock: component not installed: %s", spec.Name)
	}

	item.state = StateStarted
	return nil
}

// Stop marks a started component as pulled (stopped but installed).
func (m *MockContainerd) Stop(name string) error {
	m.ensure()
	m.mu.Lock()
	defer m.mu.Unlock()

	m.logger.Infow("Mock Stop", "name", name)

	if item, ok := m.items[name]; ok {
		item.state = StatePulled // stopped but installed
	}
	return nil
}

// Delete removes the component from memory.
func (m *MockContainerd) Delete(name string) error {
	m.ensure()
	m.mu.Lock()
	defer m.mu.Unlock()

	m.logger.Infow("Mock Delete", "name", name)

	delete(m.items, name)
	return nil
}

// Status maps the in-memory state to a ComponentStatus.
func (m *MockContainerd) Status(name string) (edgeruntime.ComponentStatus, error) {
	m.ensure()
	m.mu.Lock()
	defer m.mu.Unlock()

	item, ok := m.items[name]
	if !ok {
		return edgeruntime.ComponentStatus{
			Name:      name,
			State:     "NotFound",
			Message:   "mock: not installed",
			Timestamp: time.Now().Unix(),
		}, nil
	}

	state := "Unknown"
	switch item.state {
	case StatePulled:
		state = "Stopped"
	case StateStarted:
		state = "Running"
	}

	return edgeruntime.ComponentStatus{
		Name:      name,
		State:     state,
		Message:   "mock-containerd",
		Timestamp: time.Now().Unix(),
	}, nil
}
