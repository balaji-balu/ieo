package postgres

import "github.com/balaji-balu/ieo/internal/co/store/postgres/ent"

// Client returns the store's ent client, for tests.
func (s *Store) Client() *ent.Client { return s.client }
