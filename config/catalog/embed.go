// Package catalog embeds the built-in spanwit domain catalog data (SSOT).
//
// The YAML here is the single source of truth for curated use domains and
// their disk-concern entries. It is validated against the domain-catalog
// schema at load time (see internal/catalog) and by `make validate-schemas`.
// This file is a plain embed carrier; it is not under the hand-edited
// internal/assets tree.
package catalogdata

import _ "embed"

//go:embed spanwit-domain-catalog.v1.yaml
var BuiltInDomainCatalogV1 []byte
