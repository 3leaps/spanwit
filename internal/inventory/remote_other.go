//go:build !darwin && !linux

package inventory

import "os"

func isDatalessDir(os.FileInfo) bool { return false }

func fsTypeOf(string) (string, bool) { return "", false }
