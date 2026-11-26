package store

import (
	"github.com/danielgtmn/ablage"
	"github.com/danielgtmn/sesam/v2"
)

// declareSignIn declares sesam's tables (auth_users, auth_sessions,
// auth_accounts, auth_verifications, auth_rate_limits) on st, so they are
// created and migrated with the panel's own and users can reference
// auth_users. The panel sets up sesam with its full configuration on the
// same store (internal/panel, initAuth); declaring the same tables again
// there changes nothing. sesam.New does not touch the database.
//
// The tables keep the names and columns sesam 1.x created, so databases
// from before keep working as they are (sesam's wiki: Upgrading to v2).
// The store has no table prefix, which sesam needs for these names.
func declareSignIn(st ablage.Store) error {
	_, err := sesam.New(sesam.Config{Store: st, BaseURL: "http://localhost"})
	return err
}
