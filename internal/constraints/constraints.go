// Package constraints decides whether a device satisfies a deployment profile's device
// constraints (SPEC §5.5). The CO checks directed targets before accepting a deployment (§8.1.1);
// the LO uses the same rules to resolve targets and place deployments (§8.4, §8.6).
package constraints

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/balaji-balu/ieo/internal/contract"
)

// ErrNotSatisfied is returned, wrapped with the first rule or requirement the device fails, when
// a device does not satisfy the constraints.
var ErrNotSatisfied = errors.New("device constraints not satisfied")

// Check reports whether the device described by caps satisfies c (SPEC §5.5): every eligibility
// rule, and the capacity requirements against the device's reported totals. It returns nil, or an
// error wrapping ErrNotSatisfied that names the first rule or requirement the device fails.
func Check(c contract.DeviceConstraints, caps contract.DeviceCapabilitiesManifest) error {
	props, err := wireProperties(caps.Properties)
	if err != nil {
		return err
	}
	for i, rule := range c.EligibilityRules {
		if err := checkSelector(rule.PropertySelector, props, propertyValue); err != nil {
			return fmt.Errorf("%w: eligibility rule %d: property %w", ErrNotSatisfied, i+1, err)
		}
		if err := checkSelector(rule.LabelSelector, caps.Labels, labelValue); err != nil {
			return fmt.Errorf("%w: eligibility rule %d: label %w", ErrNotSatisfied, i+1, err)
		}
	}
	if c.CapacityRequirements != nil {
		if err := checkCapacity(*c.CapacityRequirements, caps.Properties); err != nil {
			return fmt.Errorf("%w: capacity: %w", ErrNotSatisfied, err)
		}
	}
	return nil
}

// wireProperties returns the device's properties as their JSON wire encoding decodes, so property
// selector keys follow the Margo field names (SPEC §5.5).
func wireProperties(p contract.DeviceCapabilities) (any, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("encode device properties: %w", err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("decode device properties: %w", err)
	}
	return v, nil
}

// A value is what a key refers to: a property, an array element or a label. lookup finds a key
// in a document and reports whether it is present.
type (
	value struct {
		v     any
		label bool
	}
	lookup[D any] func(doc D, key string) (value, bool)
)

func propertyValue(doc any, key string) (value, bool) {
	v, ok := resolvePointer(doc, key)
	return value{v: v}, ok
}

func labelValue(labels map[string]string, key string) (value, bool) {
	v, ok := labels[key]
	return value{v: v, label: true}, ok
}

// checkSelector returns nil when every expression of s matches doc; a nil selector matches.
func checkSelector[D any](s *contract.Selector, doc D, find lookup[D]) error {
	if s == nil {
		return nil
	}
	for _, e := range s.MatchExpressions {
		if !matches(e, doc, find) {
			return fmt.Errorf("expression %s %s %v does not match", e.Key, e.Operator, e.Values)
		}
	}
	return nil
}

func matches[D any](e contract.MatchExpression, doc D, find lookup[D]) bool {
	v, present := find(doc, e.Key)
	switch e.Operator {
	case "Exists":
		return present
	case "DoesNotExist":
		return !present
	case "NotIn":
		return !present || !slices.ContainsFunc(e.Values, v.equals)
	}
	if !present {
		return false
	}
	switch e.Operator {
	case "In":
		return slices.ContainsFunc(e.Values, v.equals)
	case "Gt", "Lt":
		if len(e.Values) != 1 {
			return false
		}
		got, ok1 := v.number()
		want, ok2 := number(e.Values[0])
		if !ok1 || !ok2 {
			return false
		}
		return e.Operator == "Gt" && got > want || e.Operator == "Lt" && got < want
	case "ContainsAll", "ContainsAny":
		items, ok := v.v.([]any)
		if !ok || e.ItemSelector == nil {
			return false
		}
		return slices.ContainsFunc(items, func(item any) bool {
			return itemMatches(e.Operator == "ContainsAll", e.ItemSelector.MatchExpressions, item)
		})
	default:
		return false // unknown operator; the pinned schema allows none
	}
}

// itemMatches reports whether all (or, when !all, any) of exprs match one array element, with keys
// relative to it.
func itemMatches(all bool, exprs []contract.MatchExpression, item any) bool {
	match := func(e contract.MatchExpression) bool { return matches(e, item, propertyValue) }
	if all {
		return !slices.ContainsFunc(exprs, func(e contract.MatchExpression) bool { return !match(e) })
	}
	return slices.ContainsFunc(exprs, match)
}

// equals reports whether v equals want: by JSON type and value for properties, by text form for
// labels, which are always strings (SPEC §5.5).
func (v value) equals(want any) bool {
	if v.label {
		text, ok := textForm(want)
		return ok && text == v.v
	}
	if a, ok := number(v.v); ok {
		b, ok := number(want)
		return ok && a == b
	}
	switch got := v.v.(type) {
	case string:
		w, ok := want.(string)
		return ok && got == w
	case bool:
		w, ok := want.(bool)
		return ok && got == w
	}
	return false
}

// number returns v as a number: a property's number, or a label's text read as a decimal.
func (v value) number() (float64, bool) {
	if v.label {
		f, err := strconv.ParseFloat(v.v.(string), 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return number(v.v)
}

// number returns a decoded JSON or YAML number as a float64.
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint64:
		return float64(n), true
	}
	return 0, false
}

// textForm is how a value compares with a label: strings as themselves, numbers and booleans as
// written in decimal or as true/false.
func textForm(v any) (string, bool) {
	if s, ok := v.(string); ok {
		return s, true
	}
	if b, ok := v.(bool); ok {
		return strconv.FormatBool(b), true
	}
	if f, ok := number(v); ok {
		return strconv.FormatFloat(f, 'f', -1, 64), true
	}
	return "", false
}

// resolvePointer resolves an RFC 6901 JSON Pointer against a decoded JSON document.
func resolvePointer(doc any, pointer string) (any, bool) {
	if pointer == "" {
		return doc, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	cur := doc
	for _, token := range strings.Split(pointer[1:], "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[token]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(token)
			if err != nil || i < 0 || i >= len(node) || token != strconv.Itoa(i) {
				return nil, false
			}
			cur = node[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// checkCapacity checks requirements against the device's reported totals (SPEC §5.5).
func checkCapacity(r contract.CapacityRequirements, p contract.DeviceCapabilities) error {
	if r.CPU != nil {
		fits := slices.ContainsFunc(p.CPUs, func(c contract.CPU) bool {
			return c.Cores >= r.CPU.Cores && (len(r.CPU.Architectures) == 0 || slices.Contains(r.CPU.Architectures, c.Architecture))
		})
		if !fits { // cores of different entries are never added [Margo]
			return fmt.Errorf("no CPU entry has %g cores of %v", r.CPU.Cores, r.CPU.Architectures)
		}
	}
	for _, q := range []struct{ what, want, have string }{
		{"memory", r.Memory, p.Memory},
		{"storage", r.Storage, p.Storage},
	} {
		if q.want == "" {
			continue
		}
		want, err := ParseQuantity(q.want)
		if err != nil {
			return fmt.Errorf("required %s: %w", q.what, err)
		}
		have, err := ParseQuantity(q.have)
		if err != nil {
			return fmt.Errorf("reported %s: %w", q.what, err)
		}
		if have < want {
			return fmt.Errorf("%s %s, want at least %s", q.what, q.have, q.want)
		}
	}
	return nil
}

var quantity = regexp.MustCompile(`^([0-9]+)(Ki|Mi|Gi|Ti|Pi|Ei)$`)

var unitShift = map[string]uint{"Ki": 10, "Mi": 20, "Gi": 30, "Ti": 40, "Pi": 50, "Ei": 60}

// ParseQuantity returns the bytes of a binary quantity such as `512Mi` (units Ki, Mi, Gi, Ti, Pi,
// Ei), as Margo writes memory and storage.
func ParseQuantity(s string) (uint64, error) {
	m := quantity.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("quantity %q is not <digits><Ki|Mi|Gi|Ti|Pi|Ei>", s)
	}
	n, err := strconv.ParseUint(m[1], 10, 64)
	shift := unitShift[m[2]]
	if err != nil || bits.LeadingZeros64(n) < int(shift) {
		return 0, fmt.Errorf("quantity %q is too large", s)
	}
	return n << shift, nil
}
