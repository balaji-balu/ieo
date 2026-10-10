package contract

import (
	"bytes"
	"embed"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
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
	v, err := validateSiteMessage[T](data)
	if err != nil {
		return msg, err
	}
	// encoding/json matches keys case-insensitively, so an unknown key such as "STATE" would
	// overwrite the validated "state". Decode only the keys that name a field exactly.
	known, err := json.Marshal(keepKnownFields(v, reflect.TypeOf(msg)))
	if err == nil {
		err = json.Unmarshal(known, &msg)
	}
	if err != nil {
		var zero T
		return zero, fmt.Errorf("%w: %s: %w", ErrInvalidMessage, schemaName[T](), err)
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
	if _, err := validateSiteMessage[T](data); err != nil {
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

// ComponentStatusEvent reports a state change of one component (EN → LO). Component is empty
// exactly when State is StateRemoving or StateRemoved: the event then holds for the whole
// deployment on the host (SPEC §11.2).
type ComponentStatusEvent struct {
	DeploymentID uuid.UUID      `json:"deploymentId"`
	Digest       Digest         `json:"digest"`
	Component    string         `json:"component,omitempty"`
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

// validateSiteMessage parses data and validates it against T's schema. It returns the parsed
// value (maps, slices, json.Number, …) for decoding.
func validateSiteMessage[T SiteMessage](data []byte) (any, error) {
	name := schemaName[T]()
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrInvalidMessage, name, err)
	}
	if err := siteSchemas()[name].Validate(v); err != nil {
		return nil, fmt.Errorf("%w: %s: %s", ErrInvalidMessage, name, schemaViolations(err))
	}
	return v, nil
}

var (
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
	jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()
)

// keepKnownFields returns v, a parsed JSON value, without the object keys that are not the exact
// JSON name of a field of t, at every depth. Values that t decodes itself (UUIDs, times, IDs,
// raw JSON, `any`) and map entries are kept whole.
func keepKnownFields(v any, t reflect.Type) any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	pt := reflect.PointerTo(t)
	if pt.Implements(textUnmarshalerType) || pt.Implements(jsonUnmarshalerType) {
		return v
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return v
		}
		out := make(map[string]any, len(obj))
		for i := range t.NumField() {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if !f.IsExported() || name == "-" || name == "" {
				continue
			}
			if fv, ok := obj[name]; ok {
				out[name] = keepKnownFields(fv, f.Type)
			}
		}
		return out
	case reflect.Slice, reflect.Array:
		arr, ok := v.([]any)
		if !ok {
			return v
		}
		out := make([]any, len(arr))
		for i, e := range arr {
			out[i] = keepKnownFields(e, t.Elem())
		}
		return out
	case reflect.Map:
		obj, ok := v.(map[string]any)
		if !ok {
			return v
		}
		out := make(map[string]any, len(obj))
		for k, e := range obj {
			out[k] = keepKnownFields(e, t.Elem())
		}
		return out
	default:
		return v
	}
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
