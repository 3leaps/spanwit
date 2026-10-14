package space

// Every byte figure in the space report states its bound from the shared
// byte_bound vocabulary (exact|lower|upper|unknown). Bounds are derived at
// marshal time from the evidence that already decides them (size_incomplete,
// size_status, probe status), so an emitted bound can never disagree with the
// flag it restates. The bound fields on the structs exist so documents load
// back (including under unknown-field rejection); whatever they hold in
// memory is overwritten on marshal.
//
// The schemas keep these fields optional so reports written before bounds
// existed still load; tests require every emitted document to carry them.
