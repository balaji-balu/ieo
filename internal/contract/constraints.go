package contract

// DeviceConstraints are a deployment profile's requirements on the device that runs it (Margo
// DeviceConstraints, SPEC §4.1.4). internal/constraints evaluates them (SPEC §5.5).
type DeviceConstraints struct {
	CapacityRequirements *CapacityRequirements `json:"capacityRequirements,omitempty"`
	EligibilityRules     []EligibilityRule     `json:"eligibilityRules,omitempty"`
}

// CapacityRequirements are the minimum CPU, memory and storage a device must report. Memory and
// Storage are binary quantities such as `512Mi`; empty means no requirement.
type CapacityRequirements struct {
	CPU     *CPURequirement `json:"cpu,omitempty"`
	Memory  string          `json:"memory,omitempty"`
	Storage string          `json:"storage,omitempty"`
}

// CPURequirement asks for Cores on one CPU entry, of one of Architectures when any are given.
type CPURequirement struct {
	Cores         float64  `json:"cores"`
	Architectures []string `json:"architectures,omitempty"`
}

// EligibilityRule matches a device's reported properties and labels; a nil selector matches.
type EligibilityRule struct {
	PropertySelector *Selector `json:"propertySelector,omitempty"`
	LabelSelector    *Selector `json:"labelSelector,omitempty"`
}

// Selector is a set of match expressions, all of which must match.
type Selector struct {
	MatchExpressions []MatchExpression `json:"matchExpressions"`
}

// MatchExpression tests one key. Operator is In, NotIn, Exists, DoesNotExist, Gt, Lt, ContainsAll
// or ContainsAny; Values serve In, NotIn, Gt and Lt, ItemSelector serves ContainsAll and
// ContainsAny.
type MatchExpression struct {
	Key          string    `json:"key"`
	Operator     string    `json:"operator"`
	Values       []any     `json:"values,omitempty"`
	ItemSelector *Selector `json:"itemSelector,omitempty"`
}
