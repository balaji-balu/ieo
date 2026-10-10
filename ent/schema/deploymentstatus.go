package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/edge"
)

// DeploymentStatus represents deployment progress at any orchestrator layer.
type DeploymentStatus struct {
	ent.Schema
}

func (DeploymentStatus) Fields() []ent.Field {
	return []ent.Field{
		UUIDField(),
		// field.UUID("id", uuid.UUID{}).
		// 	Default(uuid.New),
		field.String("state").Default("pending"),
		field.String("error_code").Optional(),
		field.String("error_message").Optional(),
		field.String("host_id").Optional(),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (DeploymentStatus) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("components", DeploymentComponentStatus.Type),
	}
}
