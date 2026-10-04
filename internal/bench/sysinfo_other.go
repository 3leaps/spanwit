//go:build !unix

package bench

// kernelVersion reports that the kernel release is not readable here. It
// returns a stated "unknown" rather than an empty string so validation
// distinguishes "the harness looked and could not tell" from "nobody looked".
func kernelVersion() string {
	return "unknown"
}
