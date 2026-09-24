// Package templates holds the files a store is created with.
//
// SCHEMA.md is the data contract. It is written into every store, so the rules
// travel with the user's memory instead of living only in this repository, and
// an agent working against a store it has never seen can still read them.
package templates

import _ "embed"

// Schema is the contract a store keeps in shared/SCHEMA.md.
//
//go:embed SCHEMA.md
var Schema string
