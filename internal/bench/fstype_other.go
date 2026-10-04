//go:build !darwin && !linux

package bench

// filesystemType is not readable portably off Darwin and Linux. Returning
// false leaves the field absent, which fails validation until an operator
// declares it: a row measured on an unidentified filesystem is not comparable
// to one measured on a known filesystem, so refusing to emit it is correct.
func filesystemType(_ string) (string, bool) {
	return "", false
}

// deviceClass is not determinable here; an operator must declare it.
func deviceClass(_ string) (DeviceClass, bool) {
	return "", false
}
