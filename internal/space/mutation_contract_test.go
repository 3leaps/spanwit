package space

import "testing"

func TestNormalizeMutationContract(t *testing.T) {
	if got, err := normalizeMutationContract(""); err != nil || got != "" {
		t.Fatalf("empty: got %q err=%v", got, err)
	}
	if got, err := normalizeMutationContract("open"); err != nil || got != "open" {
		t.Fatalf("open: got %q err=%v", got, err)
	}
	if got, err := normalizeMutationContract("read_only"); err != nil || got != "read_only" {
		t.Fatalf("read_only: got %q err=%v", got, err)
	}
	if _, err := normalizeMutationContract("write"); err == nil {
		t.Fatal("invalid must error")
	}
}
