// Package reportmodel holds spanwit's emitted report types and their bounded
// loaders: the space report, capacity-only report, compare report, exact prune
// plan and container runtime section. It is a leaf: it reaches no collector,
// process spawn, socket or delete path, so a read-only viewer can depend on it
// alone. The boundary tests in this package pin that.
package reportmodel
