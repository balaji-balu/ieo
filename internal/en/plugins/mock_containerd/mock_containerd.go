package mockcontainerd

import (
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/balaji-balu/ieo/pkg/en/edgeruntime"
	"github.com/balaji-balu/ieo/internal/en/plugins"
	"github.com/balaji-balu/ieo/pkg/logx"
)

func init() {
	plugins.Register(&MockContainerd{})
}

type mockState string

const (
	StateNone    mockState = "None"
	StatePulled  mockState = "Pulled"
	StateStarted mockState = "Started"
)

type container struct {
	spec  edgeruntime.ComponentSpec
	state mockState
}

type MockContainerd struct {
	mu     sync.Mutex
	items  map[string]*container
	logger *zap.SugaredLogger
}

func (m *MockContainerd) Name() string {
	return "mock-containerd"
}

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

/* ================
   INSTALL (fake pull)
================ */
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

/* ================
   START (fake start)
================ */
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

/* ================
   STOP (fake stop)
================ */
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

/* ================
   DELETE (remove from memory)
================ */
func (m *MockContainerd) Delete(name string) error {
	m.ensure()
	m.mu.Lock()
	defer m.mu.Unlock()

	m.logger.Infow("Mock Delete", "name", name)

	delete(m.items, name)
	return nil
}

/* ================
   STATUS (simple mapping)
================ */
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
