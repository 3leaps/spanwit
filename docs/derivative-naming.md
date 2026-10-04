---
title: "Derivative Naming & Template Reference"
description: "How to name a derivative project and where the upstream Fulmen microtool template lives"
last_updated: "2026-05-29"
---

# Derivative Naming & Template Reference

Spanwit is built on the **Fulmen microtool** pattern (Go + [gofulmen](https://github.com/fulmenhq/gofulmen) + Crucible standards). This page is the concrete reference behind the trademark courtesy in [`LICENSE`](../LICENSE): which standard spanwit follows, and how to rename a derivative so it doesn't reuse 3 Leaps marks.

## Upstream template

Spanwit follows the [Fulmen Forge Microtool Standard](https://github.com/fulmenhq/crucible/blob/main/docs/architecture/fulmen-forge-microtool-standard.md). If you are starting a _new_ microtool, follow that standard rather than copying this repository.

## Renaming a derivative

Per the trademark notice in [`LICENSE`](../LICENSE), as a courtesy avoid using `3leaps`, `fulmen`, `fulmens`, `fulmenhq`, or `spanwit` in repository or package names. When you refit a derivative, change the identity in these places:

| What                                             | Where                                                   |
| ------------------------------------------------ | ------------------------------------------------------- |
| Binary name, env-var prefix, config name, vendor | `.fulmen/app.yaml` (then `make sync-embedded-identity`) |
| Go module path                                   | `go.mod` (`module …`) and all imports                   |
| Command directory                                | `cmd/<your-tool>/`                                      |
| Marks in docs/README                             | `README.md`, `MAINTAINERS.md`, `docs/`                  |

After refitting, confirm the identity:

```bash
make build && ./bin/<your-tool> envinfo   # shows binary name, vendor, env prefix
make pr-final                             # full non-mutating gate
```

## See also

- [Spanwit Overview](./spanwit-overview.md)
- [Fulmen Forge Microtool Standard](https://github.com/fulmenhq/crucible/blob/main/docs/architecture/fulmen-forge-microtool-standard.md)
