package contract

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ErrInvalidMessage is returned, wrapped with the reason, for a site message that is not valid
// JSON or fails its schema (SPEC §11.2). A receiver logs and drops such a message; it MUST NOT
// change state.
var ErrInvalidMessage = errors.New("invalid site message")

// SiteMessage is the set of LO ↔ EN message payloads (SPEC §11.2).
type SiteMessage interface {
	Command | CommandAck | ComponentStatusEvent | Inventory | Heartbeat | Capabilities | InventoryRequest
}

// DecodeSiteMessage validates data against T's schema and decodes it. Unknown fields are
// ignored. On any failure it returns the zero T and an error wrapping ErrInvalidMessage.
func DecodeSiteMessage[T SiteMessage](data []byte) (T, error) {
	var msg T
	if err := validateSiteMessage[T](data); err != nil {
		return msg, err
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		var zero T
		return zero, fmt.Errorf("%w: %T: %w", ErrInvalidMessage, zero, err)
	}
	return msg, nil
}

// EncodeSiteMessage encodes msg and validates the result against T's schema, so a sender never
// publishes a message receivers would drop. A schema failure wraps ErrInvalidMessage.
func EncodeSiteMessage[T SiteMessage](msg T) ([]byte, error) {
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("encode %T: %w", msg, err)
	}
	if err := validateSiteMessage[T](data); err != nil {
		return nil, err
	}
	return data, nil
}

// Action is what a Command asks the EN to do with a deployment.
type Action string

// Command actions (SPEC §8.9).
const (
	ActionApply  Action = "apply"
	ActionRemove Action = "remove"
)

// Command asks an EN to apply or remove one deployment (LO → EN, request/reply). Deployment is
// set for ActionApply only.
type Command struct {
	CommandID    uuid.UUID              `json:"commandId"`
	Action       Action                 `json:"action"`
	DeploymentID uuid.UUID              `json:"deploymentId"`
	Digest       Digest                 `json:"digest"`
	Deployment   *ApplicationDeployment `json:"deployment,omitempty"`
}

// CommandAck is the EN's reply to a Command: acceptance only, outcomes arrive as
// ComponentStatusEvents. Error is set exactly when Accepted is false.
type CommandAck struct {
	CommandID uuid.UUID `json:"commandId"`
	Accepted  bool      `json:"accepted"`
	Error     *AckError `json:"error,omitempty"`
}

// AckError says why an EN rejected a Command.
type AckError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ComponentStatusEvent reports a state change of one component (EN → LO).
type ComponentStatusEvent struct {
	DeploymentID uuid.UUID      `json:"deploymentId"`
	Digest       Digest         `json:"digest"`
	Component    string         `json:"component"`
	State        ComponentState `json:"state"`
	Error        *StatusError   `json:"error,omitempty"`
	At           time.Time      `json:"at"`
}

// Inventory is an EN's complete actual state (EN → LO). Nil Deployments encodes as `[]`.
type Inventory struct {
	HostID      HostID                `json:"hostId"`
	At          time.Time             `json:"at"`
	Deployments []InventoryDeployment `json:"deployments"`
}

// MarshalJSON encodes nil Deployments as an empty array, as the schema requires.
func (m Inventory) MarshalJSON() ([]byte, error) {
	type plain Inventory
	if m.Deployments == nil {
		m.Deployments = []InventoryDeployment{}
	}
	return json.Marshal(plain(m))
}

// InventoryDeployment is one deployment in an Inventory. Nil Components encodes as `[]`.
type InventoryDeployment struct {
	DeploymentID uuid.UUID         `json:"deploymentId"`
	Digest       Digest            `json:"digest"`
	Components   []ComponentStatus `json:"components"`
}

// MarshalJSON encodes nil Components as an empty array, as the schema requires.
func (d InventoryDeployment) MarshalJSON() ([]byte, error) {
	type plain InventoryDeployment
	if d.Components == nil {
		d.Components = []ComponentStatus{}
	}
	return json.Marshal(plain(d))
}

// Heartbeat tells the LO a host is alive (EN → LO).
type Heartbeat struct {
	HostID        HostID    `json:"hostId"`
	At            time.Time `json:"at"`
	UptimeSeconds uint64    `json:"uptimeSeconds"`
}

// Capabilities reports a host's capabilities and labels (EN → LO). Nil Labels encodes as `{}`.
type Capabilities struct {
	HostID       HostID             `json:"hostId"`
	Capabilities DeviceCapabilities `json:"capabilities"`
	Labels       map[string]string  `json:"labels"`
}

// MarshalJSON encodes nil Labels as an empty object, as the schema requires.
func (m Capabilities) MarshalJSON() ([]byte, error) {
	type plain Capabilities
	if m.Labels == nil {
		m.Labels = map[string]string{}
	}
	return json.Marshal(plain(m))
}

// InventoryRequest asks every EN of a site to publish its Inventory (LO → all ENs).
type InventoryRequest struct{}

// SPEC §11.2: the normative schemas, one per message.
//
//go:embed schemas/site/*.schema.json
var siteSchemaFiles embed.FS

const siteSchemaBase = "https://ieo.invalid/schemas/site/"

// siteSchemas compiles the embedded schemas once. They are fixed at build time and covered by
// tests, so a failure here is a programmer error (G-B4).
var siteSchemas = sync.OnceValue(func() map[string]*jsonschema.Schema {
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	names, err := fs.Glob(siteSchemaFiles, "schemas/site/*.schema.json")
	if err != nil {
		panic(err)
	}
	for _, name := range names {
		b, err := siteSchemaFiles.ReadFile(name)
		if err != nil {
			panic(err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			panic(fmt.Sprintf("%s: %v", name, err))
		}
		if err := c.AddResource(siteSchemaBase+name[len("schemas/site/"):], doc); err != nil {
			panic(fmt.Sprintf("%s: %v", name, err))
		}
	}
	out := map[string]*jsonschema.Schema{}
	for _, file := range []string{
		"command", "command-ack", "component-status-event", "inventory", "heartbeat",
		"capabilities", "inventory-request",
	} {
		out[file] = c.MustCompile(siteSchemaBase + file + ".schema.json")
	}
	return out
})

// schemaName returns the schema file name (without .schema.json) of message type T.
func schemaName[T SiteMessage]() string {
	var zero T
	switch any(zero).(type) {
	case Command:
		return "command"
	case CommandAck:
		return "command-ack"
	case ComponentStatusEvent:
		return "component-status-event"
	case Inventory:
		return "inventory"
	case Heartbeat:
		return "heartbeat"
	case Capabilities:
		return "capabilities"
	default: // InventoryRequest; the constraint admits no other type.
		return "inventory-request"
	}
}

func validateSiteMessage[T SiteMessage](data []byte) error {
	name := schemaName[T]()
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalidMessage, name, err)
	}
	if err := siteSchemas()[name].Validate(v); err != nil {
		return fmt.Errorf("%w: %s: %s", ErrInvalidMessage, name, schemaViolations(err))
	}
	return nil
}

// schemaViolations returns the violations of a validation error on one line, e.g.
// "at '/digest': 'sha256:00' does not match pattern …; at '/commandId': …", for the log entry of
// a dropped message (SPEC §11.2).
func schemaViolations(err error) string {
	lines := strings.Split(err.Error(), "\n")
	if len(lines) > 1 {
		lines = lines[1:] // the first line only names the schema URL
	}
	for i, l := range lines {
		lines[i] = strings.TrimLeft(l, " -")
	}
	return strings.Join(lines, "; ")
}
