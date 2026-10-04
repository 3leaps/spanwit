package schema

import _ "embed"

//go:embed spanwit-config.v1.schema.json
var SpanwitConfigV1 []byte

//go:embed spanwit-prune-plan.v1.schema.json
var SpanwitPrunePlanV1 []byte

//go:embed spanwit-space-report.v1.schema.json
var SpanwitSpaceReportV1 []byte

//go:embed spanwit-space-report.v2.schema.json
var SpanwitSpaceReportV2 []byte

//go:embed spanwit-space-compare.v1.schema.json
var SpanwitSpaceCompareV1 []byte

//go:embed spanwit-domain-catalog.v1.schema.json
var SpanwitDomainCatalogV1 []byte

//go:embed spanwit-filesystem-inventory.v0.schema.json
var SpanwitFilesystemInventoryV0 []byte

//go:embed spanwit-filesystem-inventory-aggregation.v0.schema.json
var SpanwitFilesystemInventoryAggregationV0 []byte

//go:embed spanwit-filesystem-inventory-aggregation.v1.schema.json
var SpanwitFilesystemInventoryAggregationV1 []byte

//go:embed spanwit-observe-state.v1.schema.json
var SpanwitObserveStateV1 []byte

//go:embed spanwit-observe-health.v1.schema.json
var SpanwitObserveHealthV1 []byte

//go:embed spanwit-filesystem-inventory-aggregation.v2.schema.json
var SpanwitFilesystemInventoryAggregationV2 []byte
