package appidentityembed

import (
	_ "embed"

	"github.com/fulmenhq/gofulmen/appidentity"
)

// Embedded app identity fallback.
//
//go:embed app.yaml
var embeddedIdentityYAML []byte

func Register() error {
	return appidentity.RegisterEmbeddedIdentityYAML(embeddedIdentityYAML)
}
