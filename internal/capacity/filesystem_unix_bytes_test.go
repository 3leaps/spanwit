//go:build unix

package capacity

import (
	"math"
	"testing"
)

func TestBlocksToBytes_RefusesOverflow(t *testing.T) {
	cases := []struct {
		blocks uint64
		bsize  int64
		want   int64
		ok     bool
	}{
		{blocks: 10, bsize: 4096, want: 40960, ok: true},
		{blocks: 0, bsize: 4096, want: 0, ok: true},
		{blocks: uint64(math.MaxInt64 / 4096), bsize: 4096, want: (math.MaxInt64 / 4096) * 4096, ok: true},
		{blocks: uint64(math.MaxInt64/4096) + 1, bsize: 4096, ok: false},
		{blocks: math.MaxUint64, bsize: 1, ok: false},
		{blocks: 1, bsize: 0, ok: false},
		{blocks: 1, bsize: -512, ok: false},
	}
	for _, c := range cases {
		got, ok := blocksToBytes(c.blocks, c.bsize)
		if ok != c.ok || got != c.want {
			t.Errorf("blocksToBytes(%d, %d) = %d, %v; want %d, %v", c.blocks, c.bsize, got, ok, c.want, c.ok)
		}
	}
}
