package schema

import (
	"time"
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/edge"
)

// DeploymentComponentStatus holds the schema definition for the DeploymentComponentStatus entity.
type DeploymentComponentStatus struct {
	ent.Schema
}

func (DeploymentComponentStatus) Fields() []ent.Field {
	return []ent.Field{
		UUIDField(),
		// field.UUID("id", uuid.UUID{}).
		// 	Default(uuid.New),
		field.String("name").NotEmpty(),
		field.String("state").Default("pending"),
		field.String("error_code").Optional(),
		field.String("error_message").Optional(),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
		//field.UUID("deployment_id", uuid.UUID{}),
	}
}

func (DeploymentComponentStatus) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("deployment", DeploymentStatus.Type).
			Ref("components").
			Unique().
			Required(),
	}
}
