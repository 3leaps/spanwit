package envelope

// builtinPatterns are the candidate patterns of spanwit's built-in
// signatures. As glob words they are fixed vocabulary, not paths; any other
// glob that names a location (a user-config pattern) is a relative path.
var builtinPatterns = map[string]bool{
	"**/target":                      true,
	"**/grype/db/grype-db-download*": true,
	"**/.gocache":                    true,
}
