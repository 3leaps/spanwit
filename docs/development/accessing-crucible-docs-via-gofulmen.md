---
title: "Accessing Crucible Docs via gofulmen"
description: "How template consumers can read Crucible standards and schemas through the gofulmen shim"
last_updated: "2025-12-17"
---

# Accessing Crucible Docs via gofulmen

Fulmen projects should prefer the `gofulmen/crucible` shim to access Crucible standards and schemas. This keeps consumption library-first and avoids requiring a separate Crucible checkout.

## Requirements

- Go 1.26+
- `github.com/fulmenhq/gofulmen` in your module

## Reading a Crucible Document

Use `crucible.GetDoc` to load a markdown document by path (relative to the Crucible docs root).

```go
import (
  "context"

  "github.com/fulmenhq/gofulmen/crucible"
)

func loadStandard(ctx context.Context) (string, error) {
  doc, err := crucible.GetDoc("standards/agentic-attribution.md")
  if err != nil {
    return "", err
  }
  return doc, nil
}
```

## Reading a Crucible Schema

Use `crucible.GetSchema` to load a JSON schema by path.

```go
import (
  "github.com/fulmenhq/gofulmen/crucible"
)

func loadSchema() ([]byte, error) {
  schemaBytes, err := crucible.GetSchema("schemas/app/app-identity.v1.0.0.json")
  if err != nil {
    return nil, err
  }
  return schemaBytes, nil
}
```

## Notes for Template Consumers (CDRL)

- If you don’t need Crucible docs/schemas at runtime, you can avoid adding any code paths that depend on them.
- In CI/container environments, avoid walking the filesystem above the workspace to find repo-relative assets; prefer the gofulmen/pathfinder repo-root helper patterns used in this template.
