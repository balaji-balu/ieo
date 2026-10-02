package contract

import (
	"encoding/json"

	"github.com/google/uuid"
)

// Media types of the Margo Workload Management API bodies (SPEC §11.1).
const (
	ManifestMediaType   = "application/vnd.margo.manifest.v1+json"
	BundleMediaType     = "application/vnd.margo.bundle.v1+tar+gzip"
	DeploymentMediaType = "application/yaml"
	ProblemMediaType    = "application/problem+json"
)

// Problem types (SPEC §11.1): the Margo problem-type registry, and about:blank for a condition it
// has no type for.
const (
	ProblemNotAuthorized      = "https://docs.margo.org/specification/problem-types#not-authorized"
	ProblemDeploymentNotFound = "https://docs.margo.org/specification/problem-types#deployment-not-found"
	ProblemInvalidBundle      = "https://docs.margo.org/specification/problem-types#invalid-bundle"
	ProblemCannotGenerate     = "https://docs.margo.org/specification/problem-types#server-cannot-generate-response"
	ProblemInvalidRequest     = "https://docs.margo.org/specification/problem-types#invalid-request"
	ProblemSemanticError      = "https://docs.margo.org/specification/problem-types#semantic-error"
	ProblemGatewayNotFound    = "https://docs.margo.org/specification/problem-types#gateway-not-found"
	ProblemDeviceNotFound     = "https://docs.margo.org/specification/problem-types#device-not-found"
	ProblemAboutBlank         = "about:blank"
)

// ManifestVersion orders the State Manifests of one site: it starts at 1 and strictly increases on
// every change to the site's deployment set (SPEC §4.1.6). Zero means "none accepted yet".
type ManifestVersion uint64

// StateManifest is the body of GET /api/v1/deployments (Margo UnsignedAppStateManifest, SPEC
// §4.1.6): every deployment assigned to one site. A nil Bundle encodes as `bundle: null`, which is
// required when Deployments is empty; nil Deployments encodes as `[]`.
type StateManifest struct {
	ManifestVersion ManifestVersion `json:"manifestVersion"`
	Deployments     []DeploymentRef `json:"deployments"`
	Bundle          *BundleRef      `json:"bundle"`
}

// MarshalJSON encodes nil Deployments as an empty array, as the Margo schema requires.
func (m StateManifest) MarshalJSON() ([]byte, error) {
	type plain StateManifest
	if m.Deployments == nil {
		m.Deployments = []DeploymentRef{}
	}
	return json.Marshal(plain(m))
}

// DeploymentRef points at one deployment's YAML in a State Manifest. URL is
// /api/v1/deployments/{deploymentId}/{digest}; SizeBytes is advisory and never used for integrity.
type DeploymentRef struct {
	DeploymentID uuid.UUID `json:"deploymentId"`
	Digest       Digest    `json:"digest"`
	SizeBytes    uint64    `json:"sizeBytes,omitempty"`
	URL          string    `json:"url"`
}

// BundleRef points at the gzip tar of all deployment YAMLs of a State Manifest. MediaType is
// BundleMediaType; URL is /api/v1/bundles/{digest}.
type BundleRef struct {
	MediaType string `json:"mediaType"`
	Digest    Digest `json:"digest"`
	SizeBytes uint64 `json:"sizeBytes,omitempty"`
	URL       string `json:"url"`
}

// DeviceCapabilitiesManifest is the body of PUT /api/v1/capabilities/{deviceId}.
type DeviceCapabilitiesManifest struct {
	Properties DeviceCapabilities `json:"properties"`
	Labels     map[string]string  `json:"labels,omitempty"`
}

// DeviceCapabilities are the Margo DeviceCapabilitiesManifest properties of one device (SPEC
// §4.1.3). The LO, a non-hosting gateway, sets only the identity fields (ID, Vendor, ModelNumber,
// SerialNumber); hosting fields left at their zero value are omitted on the wire.
type DeviceCapabilities struct {
	ID                       DeviceID                 `json:"id"`
	Vendor                   string                   `json:"vendor"`
	ModelNumber              string                   `json:"modelNumber"`
	SerialNumber             string                   `json:"serialNumber"`
	CPUs                     []CPU                    `json:"cpus,omitempty"`
	Memory                   string                   `json:"memory,omitempty"`
	Storage                  string                   `json:"storage,omitempty"`
	Peripherals              []Peripheral             `json:"peripherals,omitempty"`
	Interfaces               []CommunicationInterface `json:"interfaces,omitempty"`
	OTelCollector            bool                     `json:"otelCollector,omitempty"`
	SupportedRuntimes        []string                 `json:"supportedRuntimes,omitempty"`
	SupportedDeploymentTypes []string                 `json:"supportedDeploymentTypes,omitempty"`
}

// CPU describes one CPU of a host. Architecture is amd64, arm64 or arm.
type CPU struct {
	Cores        float64 `json:"cores"`
	Architecture string  `json:"architecture,omitempty"`
}

// Peripheral is a device attached to a host. Type is gpu, display, camera, microphone or speaker.
type Peripheral struct {
	Type         string `json:"type"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
}

// CommunicationInterface is a host network or bus interface, e.g. ethernet, wifi or canbus.
type CommunicationInterface struct {
	Type string `json:"type"`
}

// ComponentState is the state of one component on a host, and of a deployment as a whole
// (SPEC §4.1.9, §7.1).
type ComponentState string

// Component and deployment states (SPEC §4.1.9).
const (
	StatePending    ComponentState = "pending"
	StateInstalling ComponentState = "installing"
	StateInstalled  ComponentState = "installed"
	StateRemoving   ComponentState = "removing"
	StateRemoved    ComponentState = "removed"
	StateFailed     ComponentState = "failed"
)

// StatusError explains a failed state: a stable code (e.g. IEO-PULL-FAILED), the tier or
// component that raised it, and a human-readable message.
type StatusError struct {
	Code    string `json:"code,omitempty"`
	Source  string `json:"source,omitempty"`
	Message string `json:"message,omitempty"`
}

// ComponentStatus is the state of one component of a deployment (SPEC §4.1.9).
type ComponentStatus struct {
	Name  string         `json:"name"`
	State ComponentState `json:"state"`
	Error *StatusError   `json:"error,omitempty"`
}

// DeploymentStatus is the body of POST /api/v1/deployments/{deploymentId}/status (Margo
// DeploymentStatusManifest, SPEC §4.1.9). Components has exactly one entry per component of the
// deployment; nil encodes as `[]`.
type DeploymentStatus struct {
	DeploymentID           uuid.UUID         `json:"deploymentId"`
	DeviceID               DeviceID          `json:"deviceId"`
	AdoptedManifestVersion ManifestVersion   `json:"adoptedManifestVersion"`
	Status                 DeploymentState   `json:"status"`
	Components             []ComponentStatus `json:"components"`
}

// MarshalJSON encodes nil Components as an empty array, as the Margo schema requires.
func (s DeploymentStatus) MarshalJSON() ([]byte, error) {
	type plain DeploymentStatus
	if s.Components == nil {
		s.Components = []ComponentStatus{}
	}
	return json.Marshal(plain(s))
}

// DeploymentState is the aggregate state of a deployment (SPEC §7.2).
type DeploymentState struct {
	State ComponentState `json:"state"`
	Error *StatusError   `json:"error,omitempty"`
}

// Problem is an RFC 9457 problem details body (application/problem+json), the error format of
// the Margo API (SPEC §11.1).
type Problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
	// Errors are field-level errors, on 422 responses (Margo extension).
	Errors []FieldError `json:"errors,omitempty"`
}

// ApplicationDeployment is one application instance assigned to one target (Margo
// ApplicationDeployment, SPEC §4.1.5). It carries the fields IEO reads; the exact bytes of a
// deployment, which its digest covers, are kept by whoever stores or forwards it, never
// re-encoded from this type.
type ApplicationDeployment struct {
	ID       uuid.UUID          `json:"id"`
	Metadata DeploymentMetadata `json:"metadata"`
	Spec     DeploymentSpec     `json:"spec"`
}

// DeploymentMetadata names a deployment and its target. DeviceID is `<site>/<host>` for a
// directed deployment or `<site>/*` for an autonomous one (SPEC §4.1.5).
type DeploymentMetadata struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
	DeviceID    DeviceID          `json:"deviceId"`
	Annotations map[string]string `json:"annotations"`
	Labels      map[string]string `json:"labels,omitempty"`
}

// DeploymentSpec says what to run: the application, its selected deployment profile and the
// parameter values.
type DeploymentSpec struct {
	ApplicationID     string                    `json:"applicationId"`
	DeploymentProfile DeploymentProfile         `json:"deploymentProfile"`
	Parameters        map[string]ParameterValue `json:"parameters,omitempty"`
}

// DeploymentProfile is the selected profile of the Application Description. DeviceConstraints
// stays raw JSON because it is copied unmodified (SPEC §4.1.5).
type DeploymentProfile struct {
	Type              string          `json:"type"`
	Components        []Component     `json:"components"`
	DeviceConstraints json.RawMessage `json:"deviceConstraints,omitempty"`
}

// Component is one Compose package of a deployment profile.
type Component struct {
	Name       string              `json:"name"`
	Properties ComponentProperties `json:"properties"`
}

// ComponentProperties locate a component's package: Repository is an oci:// reference and
// Revision is the OCI tag (SPEC §4.2). Wait defaults to true when nil; Timeout has the form
// `##m##s`.
type ComponentProperties struct {
	Repository string `json:"repository"`
	Revision   string `json:"revision"`
	Wait       *bool  `json:"wait,omitempty"`
	Timeout    string `json:"timeout,omitempty"`
}

// ParameterValue is a parameter's value and the places in component packages it is written to.
type ParameterValue struct {
	Value   any               `json:"value"`
	Targets []ParameterTarget `json:"targets"`
}

// ParameterTarget names a JSON pointer and the components it applies to.
type ParameterTarget struct {
	Pointer    string   `json:"pointer"`
	Components []string `json:"components"`
}
