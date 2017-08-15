package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/zero-os/0-Disk/tlog"
	"github.com/zero-os/0-Disk/tlog/tlogclient"
)

// wait for sequence seqWait to be flushed
func testClientWaitSeqFlushed(ctx context.Context, t *testing.T, respChan <-chan *tlogclient.Result,
	cancelFunc func(), seqWait uint64, exactSeq bool) {

	for {
		select {
		case re := <-respChan:
			if !assert.Nil(t, re.Err) {
				cancelFunc()
				return
			}
			status := re.Resp.Status
			if !assert.Equal(t, true, status > 0) {
				continue
			}

			if status == tlog.BlockStatusFlushOK {
				seqs := re.Resp.Sequences
				seq := seqs[len(seqs)-1]

				if exactSeq && seq == seqWait {
					return
				}
				if !exactSeq && seq >= seqWait { // we've received all sequences
					return
				}
			}
		case <-ctx.Done():
			return
		}
	}

}
