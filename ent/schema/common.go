package schema

import (
	"github.com/google/uuid"
	"entgo.io/ent/schema/field"
	"entgo.io/ent"
)

// UUIDField returns a reusable UUID id field.
func UUIDField() ent.Field {
	return field.UUID("id", uuid.UUID{}).Default(uuid.New)
}
