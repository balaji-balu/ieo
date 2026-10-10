package contract_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
)

const (
	testUUID   = "6f1c2a9e-3b7d-4c8e-9f10-2a3b4c5d6e7f"
	testUUID2  = "0b8e4c1a-7d2f-4e3a-8b9c-1d2e3f4a5b6c"
	testDigest = "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
)

// decodeAs adapts contract.DecodeSiteMessage[T] to one signature so cases of every kind share a
// table.
func decodeAs[T contract.SiteMessage]() func([]byte) (any, error) {
	return func(b []byte) (any, error) { return contract.DecodeSiteMessage[T](b) }
}

// SPEC §17.1: "Site messages validate against the schemas in §11.2; unknown fields are ignored;
// invalid messages are dropped without state change."
//
// At this layer "dropped without state change" means DecodeSiteMessage returns ErrInvalidMessage
// and a zero value, so a receiver has nothing to apply; receivers are tested in their own slices.
func TestSpec_17_1_SiteMessagesValidateAgainstSchemas(t *testing.T) {
	apply := `{"commandId":"` + testUUID + `","action":"apply","deploymentId":"` + testUUID2 +
		`","digest":"` + testDigest + `","deployment":` + deploymentJSON + `}`
	remove := `{"commandId":"` + testUUID + `","action":"remove","deploymentId":"` + testUUID2 +
		`","digest":"` + testDigest + `"}`
	ackOK := `{"commandId":"` + testUUID + `","accepted":true}`
	ackNo := `{"commandId":"` + testUUID + `","accepted":false,"error":{"code":"IEO-BUSY","message":"m"}}`
	status := `{"deploymentId":"` + testUUID2 + `","digest":"` + testDigest +
		`","component":"web","state":"failed","error":{"code":"IEO-PULL-FAILED","source":"en","message":"m"},` +
		`"at":"2026-10-01T12:00:00Z"}`
	removal := func(state string) string {
		return `{"deploymentId":"` + testUUID2 + `","digest":"` + testDigest + `","state":"` + state +
			`","at":"2026-10-01T12:00:00Z"}`
	}
	inventory := `{"hostId":"host-03","at":"2026-10-01T12:00:00Z","deployments":[{"deploymentId":"` + testUUID2 +
		`","digest":"` + testDigest + `","components":[{"name":"web","state":"installed"}]}]}`
	heartbeat := `{"hostId":"host-03","at":"2026-10-01T12:00:00+02:00","uptimeSeconds":12345}`
	capabilities := `{"hostId":"host-03","capabilities":{"id":"site-1/host-03","vendor":"v","modelNumber":"m",` +
		`"serialNumber":"s","cpus":[{"cores":4,"architecture":"amd64"}],"memory":"8Gi","storage":"64Gi",` +
		`"otelCollector":true,"supportedRuntimes":["oci"],"supportedDeploymentTypes":["compose"]},` +
		`"labels":{"line":"2"}}`

	tests := []struct {
		name   string
		decode func([]byte) (any, error)
		in     string
		valid  bool
	}{
		{"command apply", decodeAs[contract.Command](), apply, true},
		{"command remove", decodeAs[contract.Command](), remove, true},
		{"command unknown field ignored", decodeAs[contract.Command](), withField(remove, `"priority":7`), true},
		{"command apply without deployment", decodeAs[contract.Command](),
			strings.Replace(remove, `"remove"`, `"apply"`, 1), false},
		{"command remove with deployment", decodeAs[contract.Command](),
			strings.Replace(apply, `"action":"apply"`, `"action":"remove"`, 1), false},
		{"command bad action", decodeAs[contract.Command](), strings.Replace(remove, `"remove"`, `"restart"`, 1), false},
		{"command bad digest", decodeAs[contract.Command](), strings.Replace(remove, "sha256:", "md5:", 1), false},
		{"command uppercase digest", decodeAs[contract.Command](), strings.Replace(remove, "sha256:9f", "sha256:9F", 1), false},
		{"command bad uuid", decodeAs[contract.Command](), strings.Replace(remove, testUUID, "not-a-uuid", 1), false},
		{"command missing commandId", decodeAs[contract.Command](), strings.Replace(remove, `"commandId"`, `"cid"`, 1), false},

		{"ack accepted", decodeAs[contract.CommandAck](), ackOK, true},
		{"ack rejected", decodeAs[contract.CommandAck](), ackNo, true},
		{"ack rejected without error", decodeAs[contract.CommandAck](), `{"commandId":"` + testUUID + `","accepted":false}`, false},
		{"ack accepted with error", decodeAs[contract.CommandAck](), strings.Replace(ackNo, "false", "true", 1), false},
		{"ack accepted wrong type", decodeAs[contract.CommandAck](), strings.Replace(ackOK, "true", `"yes"`, 1), false},

		{"status", decodeAs[contract.ComponentStatusEvent](), status, true},
		{"status unknown field ignored", decodeAs[contract.ComponentStatusEvent](), withField(status, `"seq":1`), true},
		{"status bad state", decodeAs[contract.ComponentStatusEvent](), strings.Replace(status, `"failed"`, `"running"`, 1), false},
		{"status bad time", decodeAs[contract.ComponentStatusEvent](), strings.Replace(status, "2026-10-01T12:00:00Z", "yesterday", 1), false},
		{"status missing component", decodeAs[contract.ComponentStatusEvent](), strings.Replace(status, `"component"`, `"comp"`, 1), false},
		// SPEC §11.2: a removal event holds for the whole deployment and names no component.
		{"status removed without component", decodeAs[contract.ComponentStatusEvent](), removal("removed"), true},
		{"status removing without component", decodeAs[contract.ComponentStatusEvent](), removal("removing"), true},
		{"status removed with component", decodeAs[contract.ComponentStatusEvent](),
			withField(removal("removed"), `"component":"web"`), false},
		{"status removing with component", decodeAs[contract.ComponentStatusEvent](),
			withField(removal("removing"), `"component":"web"`), false},
		{"status installed without component", decodeAs[contract.ComponentStatusEvent](),
			strings.Replace(removal("removed"), `"removed"`, `"installed"`, 1), false},
		{"status empty component", decodeAs[contract.ComponentStatusEvent](),
			strings.Replace(status, `"component":"web"`, `"component":""`, 1), false},

		{"inventory", decodeAs[contract.Inventory](), inventory, true},
		{"inventory empty", decodeAs[contract.Inventory](), `{"hostId":"host-03","at":"2026-10-01T12:00:00Z","deployments":[]}`, true},
		{"inventory missing deployments", decodeAs[contract.Inventory](), `{"hostId":"host-03","at":"2026-10-01T12:00:00Z"}`, false},
		{"inventory bad host", decodeAs[contract.Inventory](), strings.Replace(inventory, "host-03", "host 03", 1), false},
		{"inventory bad component state", decodeAs[contract.Inventory](), strings.Replace(inventory, "installed", "up", 1), false},

		{"heartbeat", decodeAs[contract.Heartbeat](), heartbeat, true},
		{"heartbeat negative uptime", decodeAs[contract.Heartbeat](), strings.Replace(heartbeat, "12345", "-1", 1), false},
		{"heartbeat fractional uptime", decodeAs[contract.Heartbeat](), strings.Replace(heartbeat, "12345", "1.5", 1), false},

		{"capabilities", decodeAs[contract.Capabilities](), capabilities, true},
		{"capabilities unknown field ignored", decodeAs[contract.Capabilities](), withField(capabilities, `"firmware":"1.0"`), true},
		{"capabilities missing labels", decodeAs[contract.Capabilities](), strings.Replace(capabilities, `,"labels":{"line":"2"}`, "", 1), false},
		{"capabilities non-string label", decodeAs[contract.Capabilities](), strings.Replace(capabilities, `"2"`, "2", 1), false},
		{"capabilities missing vendor", decodeAs[contract.Capabilities](), strings.Replace(capabilities, `"vendor":"v",`, "", 1), false},

		{"inventory request", decodeAs[contract.InventoryRequest](), `{}`, true},
		{"inventory request array", decodeAs[contract.InventoryRequest](), `[]`, false},

		{"malformed json", decodeAs[contract.Heartbeat](), `{"hostId":`, false},
		{"empty payload", decodeAs[contract.Heartbeat](), ``, false},
	}
	// Unknown fields are ignored even when they differ from a known field only in case:
	// encoding/json alone would let "STATE" overwrite the validated "state".
	t.Run("case-variant unknown fields ignored", func(t *testing.T) {
		got, err := contract.DecodeSiteMessage[contract.ComponentStatusEvent](
			[]byte(withField(status, `"STATE":"bogus","Component":"","ERROR":{"code":"x"}`)))
		if err != nil {
			t.Fatal(err)
		}
		if got.State != contract.StateFailed || got.Component != "web" || got.Error.Code != "IEO-PULL-FAILED" {
			t.Errorf("decoded %+v (error %+v); case-variant keys overrode validated fields", got, got.Error)
		}
		inv, err := contract.DecodeSiteMessage[contract.Inventory]([]byte(
			strings.Replace(inventory, `"state":"installed"`, `"state":"installed","State":"up"`, 1)))
		if err != nil {
			t.Fatal(err)
		}
		if s := inv.Deployments[0].Components[0].State; s != contract.StateInstalled {
			t.Errorf("nested component state = %q, want %q", s, contract.StateInstalled)
		}
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.decode([]byte(tt.in))
			if tt.valid {
				if err != nil {
					t.Fatalf("valid message rejected: %v", err)
				}
				return
			}
			if !errors.Is(err, contract.ErrInvalidMessage) {
				t.Fatalf("invalid message: err = %v, want ErrInvalidMessage", err)
			}
			if !reflect.ValueOf(got).IsZero() {
				t.Errorf("invalid message: got %+v, want the zero value", got)
			}
		})
	}
}

// TestSiteMessageRoundTrip checks that EncodeSiteMessage produces what DecodeSiteMessage accepts,
// and that it refuses to encode a message its schema rejects.
// A sender can't publish a status event receivers would drop: a removal event with a component,
// or any other without one (SPEC §11.2).
func TestEncodeStatusEventComponentRule(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	event := func(state contract.ComponentState, component string) contract.ComponentStatusEvent {
		return contract.ComponentStatusEvent{
			DeploymentID: uuid.MustParse(testUUID2), Digest: testDigest, Component: component, State: state, At: at,
		}
	}
	cases := []struct {
		name  string
		event contract.ComponentStatusEvent
		valid bool
	}{
		{"removed, whole deployment", event(contract.StateRemoved, ""), true},
		{"removing, whole deployment", event(contract.StateRemoving, ""), true},
		{"removed, one component", event(contract.StateRemoved, "web"), false},
		{"removing, one component", event(contract.StateRemoving, "web"), false},
		{"installed, one component", event(contract.StateInstalled, "web"), true},
		{"installed, no component", event(contract.StateInstalled, ""), false},
		{"installing, no component", event(contract.StateInstalling, ""), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := contract.EncodeSiteMessage(c.event)
			if c.valid != (err == nil) {
				t.Fatalf("EncodeSiteMessage error = %v, want valid = %v", err, c.valid)
			}
			if !c.valid {
				if !errors.Is(err, contract.ErrInvalidMessage) {
					t.Errorf("error = %v, want one wrapping contract.ErrInvalidMessage", err)
				}
				return
			}
			got, err := contract.DecodeSiteMessage[contract.ComponentStatusEvent](b)
			if err != nil {
				t.Fatalf("decode what was encoded: %v", err)
			}
			if got.Component != c.event.Component || got.State != c.event.State {
				t.Errorf("round trip gave %+v, want %+v", got, c.event)
			}
		})
	}
}

func TestSiteMessageRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	inv := contract.Inventory{HostID: mustHost(t, "host-03"), At: at}
	b, err := contract.EncodeSiteMessage(inv)
	if err != nil {
		t.Fatalf("encode inventory with no deployments: %v", err)
	}
	if !strings.Contains(string(b), `"deployments":[]`) {
		t.Errorf("encoded inventory = %s, want an empty deployments array", b)
	}
	got, err := contract.DecodeSiteMessage[contract.Inventory](b)
	if err != nil {
		t.Fatalf("decode encoded inventory: %v", err)
	}
	if !got.At.Equal(at) || got.HostID != inv.HostID || len(got.Deployments) != 0 {
		t.Errorf("round trip = %+v, want %+v", got, inv)
	}

	caps := contract.Capabilities{HostID: mustHost(t, "host-03"), Capabilities: contract.DeviceCapabilities{
		ID: mustDevice(t, "site-1/host-03"), Vendor: "v", ModelNumber: "m", SerialNumber: "s",
	}}
	if b, err := contract.EncodeSiteMessage(caps); err != nil || !strings.Contains(string(b), `"labels":{}`) {
		t.Errorf("encode capabilities without labels = %s, %v; want empty labels object", b, err)
	}

	cmd := contract.Command{
		CommandID: uuid.MustParse(testUUID), Action: contract.ActionApply,
		DeploymentID: uuid.MustParse(testUUID2), Digest: contract.Digest(testDigest),
	}
	if _, err := contract.EncodeSiteMessage(cmd); !errors.Is(err, contract.ErrInvalidMessage) {
		t.Errorf("encode apply without deployment: err = %v, want ErrInvalidMessage", err)
	}
}

func withField(obj, field string) string { return obj[:len(obj)-1] + "," + field + "}" }

func mustHost(t *testing.T, s string) contract.HostID {
	t.Helper()
	h, err := contract.ParseHostID(s)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func mustDevice(t *testing.T, s string) contract.DeviceID {
	t.Helper()
	d, err := contract.ParseDeviceID(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
