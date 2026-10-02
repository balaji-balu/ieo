// Package schema is the ent schema of the CO's Postgres store (SPEC §12). Every table is prefixed
// `co_` to stay apart from the tables of the Git-based code (ADR 0002). A change here needs a new
// migration in ../../migrations; TestMigrationsMatchSchema checks the two agree.
//
// JSON documents the store encodes itself (capabilities, parameters, statuses) are bytea, not
// jsonb: jsonb refuses the \u0000 escape a reported string may contain, and the store never
// queries inside them.
package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

func table(name string) []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: name}}
}

// App is an imported application version.
type App struct{ ent.Schema }

func (App) Annotations() []schema.Annotation { return table("co_apps") }

func (App) Fields() []ent.Field {
	return []ent.Field{
		field.String("app_id").Immutable(),
		field.String("version").Immutable(),
		field.String("repository").Immutable(),
		field.Bytes("description").Immutable(),
		field.String("digest").Immutable(),
	}
}

func (App) Indexes() []ent.Index {
	return []ent.Index{index.Fields("app_id", "version").Unique().StorageKey("co_apps_app_id_version")}
}

// Site is a site with its published manifest and the SHA-256 of its token.
type Site struct{ ent.Schema }

func (Site) Annotations() []schema.Annotation { return table("co_sites") }

func (Site) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Immutable(),
		field.Bool("retired").Default(false),
		// manifest_version is 0 until the site's first manifest. A trigger refuses to lower it.
		field.Uint64("manifest_version").Default(0),
		field.Bytes("manifest_body").Optional(),
		field.String("manifest_etag").Default(""),
		field.String("manifest_bundle").Default(""),
		field.Bytes("token_hash").Optional().Nillable().Unique(),
	}
}

func (Site) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("devices", Device.Type),
		edge.To("deployments", Deployment.Type),
		edge.To("publications", Publication.Type),
	}
}

// Device is the latest capabilities report of a site's gateway (host "") or of one of its hosts.
type Device struct{ ent.Schema }

func (Device) Annotations() []schema.Annotation { return table("co_devices") }

func (Device) Fields() []ent.Field {
	return []ent.Field{
		field.String("site_id").Immutable(),
		field.String("host").Immutable(),
		field.Bytes("capabilities"),
	}
}

func (Device) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("site", Site.Type).Ref("devices").Field("site_id").Unique().Required().Immutable(),
	}
}

func (Device) Indexes() []ent.Index {
	return []ent.Index{index.Fields("site_id", "host").Unique().StorageKey("co_devices_site_id_host")}
}

// Deployment is a deployment of a site, deleted ones included, with its current status.
type Deployment struct{ ent.Schema }

func (Deployment) Annotations() []schema.Annotation { return table("co_deployments") }

func (Deployment) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Immutable(),
		field.String("site_id").Immutable(),
		field.String("target"),
		field.String("app_id"),
		field.String("app_version"),
		field.String("profile"),
		field.String("name"),
		field.String("namespace"),
		field.Bytes("parameters"),
		field.String("digest"),
		field.Uint64("digest_version"),
		field.Bool("deleted"),
		field.Uint64("deleted_version"),
		field.Bool("removed"),
		field.Bool("reported").Default(false),
		field.Bytes("current_status").Optional().Nillable(),
	}
}

func (Deployment) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("site", Site.Type).Ref("deployments").Field("site_id").Unique().Required().Immutable(),
		edge.To("statuses", Status.Type),
	}
}

func (Deployment) Indexes() []ent.Index {
	return []ent.Index{index.Fields("site_id").StorageKey("co_deployments_site_id")}
}

// Status is one status report of a deployment; ids order the reports.
type Status struct{ ent.Schema }

func (Status) Annotations() []schema.Annotation { return table("co_statuses") }

func (Status) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("deployment_id", uuid.UUID{}).Immutable(),
		field.Bytes("status").Immutable(),
	}
}

func (Status) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("deployment", Deployment.Type).Ref("statuses").Field("deployment_id").Unique().Required().Immutable(),
	}
}

func (Status) Indexes() []ent.Index {
	return []ent.Index{index.Fields("deployment_id").StorageKey("co_statuses_deployment_id")}
}

// Blob is immutable content by digest: deployment YAML and bundles.
type Blob struct{ ent.Schema }

func (Blob) Annotations() []schema.Annotation { return table("co_blobs") }

func (Blob) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Immutable(),
		field.Bytes("content").Immutable(),
	}
}

// Publication is a digest a site published in a manifest: a deployment's YAML, or a bundle
// (deployment_id is the nil UUID), so the API serves a site only its own content (SPEC §11.1).
type Publication struct{ ent.Schema }

func (Publication) Annotations() []schema.Annotation { return table("co_publications") }

func (Publication) Fields() []ent.Field {
	return []ent.Field{
		field.String("site_id").Immutable(),
		field.Bool("bundle").Immutable(),
		field.UUID("deployment_id", uuid.UUID{}).Immutable(),
		field.String("digest").Immutable(),
	}
}

func (Publication) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("site", Site.Type).Ref("publications").Field("site_id").Unique().Required().Immutable(),
	}
}

func (Publication) Indexes() []ent.Index {
	return []ent.Index{index.Fields("site_id", "bundle", "deployment_id", "digest").Unique().StorageKey("co_publications_key")}
}
