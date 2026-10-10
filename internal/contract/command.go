package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// ErrInvalidDeployment is returned, wrapped with the reason, for a Command that is a valid site
// message but whose `deployment` an EN must not apply (SPEC §8.9 step 2). The EN acks such a
// command `accepted: false` with CodeInvalidCommand and changes nothing.
var ErrInvalidDeployment = errors.New("invalid deployment in command")

// CodeInvalidCommand is the CommandAck error code for ErrInvalidDeployment (SPEC §10). It is
// never a component status code.
const CodeInvalidCommand = "IEO-INVALID-COMMAND"

// DecodeCommand validates and decodes a Command as an EN receives it (SPEC §8.9 step 2, §11.2).
//
// A message that is not a valid Command wraps ErrInvalidMessage: the EN logs and drops it. A
// valid Command whose action is `apply` must also carry a deployment the EN can apply; if not,
// the error wraps ErrInvalidDeployment and the returned Command has its CommandID, Action,
// DeploymentID and Digest set and no Deployment, so the EN can ack it. The deployment is valid
// only if it validates against the pinned Margo appDeploymentManifest schema, its `id` is the
// command's `deploymentId`, its profile type is `compose`, every component `timeout` is a
// duration, and its components have non-empty slugs that all differ, so no two share a Compose
// project (SPEC §4.2).
func DecodeCommand(data []byte) (Command, error) {
	v, err := validateSiteMessage[Command](data)
	if err != nil {
		return Command{}, err
	}
	// The site schema says only that `deployment` is an object. It is decoded apart from the rest,
	// so a deployment that does not decode is an invalid deployment, not an invalid message.
	fields, _ := v.(map[string]any)
	raw, apply := fields["deployment"]
	delete(fields, "deployment")
	var cmd Command
	if err := decodeKnown(fields, &cmd); err != nil {
		return Command{}, fmt.Errorf("%w: command: %w", ErrInvalidMessage, err)
	}
	if !apply {
		return cmd, nil
	}
	d, err := decodeCommandDeployment(raw, cmd)
	if err != nil {
		return cmd, fmt.Errorf("%w: %w", ErrInvalidDeployment, err)
	}
	cmd.Deployment = &d
	return cmd, nil
}

// decodeCommandDeployment checks and decodes the deployment of an Apply (SPEC §8.9 step 2).
func decodeCommandDeployment(raw any, cmd Command) (ApplicationDeployment, error) {
	if err := wfmSchemas()["appDeploymentManifest"].Validate(raw); err != nil {
		return ApplicationDeployment{}, errors.New(schemaViolations(err))
	}
	var d ApplicationDeployment
	if err := decodeKnown(raw, &d); err != nil {
		return ApplicationDeployment{}, errors.New("a value does not have the expected form")
	}
	if d.ID != cmd.DeploymentID {
		return ApplicationDeployment{}, fmt.Errorf("deployment id %s is not the command's deploymentId %s", d.ID, cmd.DeploymentID)
	}
	if d.Spec.DeploymentProfile.Type != "compose" {
		return ApplicationDeployment{}, fmt.Errorf("deployment profile type %q is not compose", d.Spec.DeploymentProfile.Type)
	}
	slugs := map[string]string{}
	for _, c := range d.Spec.DeploymentProfile.Components {
		slug := ComponentSlug(c.Name)
		if slug == "" {
			return ApplicationDeployment{}, errors.New("a component has no name")
		}
		if other, taken := slugs[slug]; taken {
			return ApplicationDeployment{}, fmt.Errorf("components %q and %q have the same Compose project name", other, c.Name)
		}
		slugs[slug] = c.Name
		if c.Properties.Timeout == "" {
			continue
		}
		if _, err := ParseComponentTimeout(c.Properties.Timeout); err != nil {
			return ApplicationDeployment{}, fmt.Errorf("component %q: %w", c.Name, err)
		}
	}
	return d, nil
}

// decodeKnown decodes v, a parsed JSON value that has passed its schema, into out. Only keys that
// name a field of out exactly are decoded: encoding/json matches keys without regard to case, so
// an unknown key such as "STATE" would otherwise overwrite the validated "state".
func decodeKnown(v, out any) error {
	known, err := json.Marshal(keepKnownFields(v, reflect.TypeOf(out).Elem()))
	if err != nil {
		return err
	}
	return json.Unmarshal(known, out)
}
