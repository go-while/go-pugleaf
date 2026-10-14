package nntp

import (
	"errors"
	"testing"
)

// TestCliTxStreamQueueDepth: decision 16, clamp(4*maxQueuedJobs*batchCheck + 1024, 4096, 1<<20) (A-nntp-client-6).
func TestCliTxStreamQueueDepth(t *testing.T) {
	cases := []struct {
		name          string
		batchCheck    int64
		maxQueuedJobs int
		want          int
	}{
		{"defaults", 100, 8, 4224},
		{"tiny", 1, 1, 4096},
		{"zero", 0, 0, 4096},
		{"negative", -5, -1, 4096},
		{"mid", 1000, 8, 33024},
		{"exact-max", 1<<18 - 256, 1, 1 << 20},
		{"huge", 100000, 500, 1 << 20},
		{"overflow-guard", 1 << 62, 1 << 30, 1 << 20},
	}
	for _, c := range cases {
		if got := StreamQueueDepth(c.batchCheck, c.maxQueuedJobs); got != c.want {
			t.Errorf("%s: StreamQueueDepth(%d, %d) = %d, want %d", c.name, c.batchCheck, c.maxQueuedJobs, got, c.want)
		}
	}
}

// TestCliTxPoolsRoundTrip: the sync.Pool-backed Get*/Recycle* keep their set/reset
// semantics (A-nntp-client-6). Assertions are on the value just recycled: sync.Pool
// may drop any Put, so the next Get is never expected to return the same pointer.
func TestCliTxPoolsRoundTrip(t *testing.T) {
	errX := errors.New("x")
	msgID := "<a@b>"
	job := &CHTTJob{JobID: 7}

	rd := GetResponseData(3, 238, "238 <a@b>", errX)
	if rd.CmdID != 3 || rd.Code != 238 || rd.Line != "238 <a@b>" || rd.Err != errX {
		t.Fatalf("GetResponseData fields = %+v", *rd)
	}
	RecycleResponseData(rd)
	if rd.CmdID != 0 || rd.Code != 0 || rd.Line != "" || rd.Err != nil {
		t.Fatalf("recycled ResponseData not reset: %+v", *rd)
	}

	ch := make(chan *TTResponse, 1)
	tts := GetTTSetup(ch)
	if tts.ResponseChan != ch {
		t.Fatalf("GetTTSetup: ResponseChan not set")
	}
	RecycleTTSetup(tts)
	if tts.ResponseChan != nil {
		t.Fatalf("recycled TTSetup not reset")
	}

	resp := GetTTResponse(job, true, errX)
	if resp.Job != job || !resp.ForceCleanUp || resp.Err != errX {
		t.Fatalf("GetTTResponse fields = %+v", *resp)
	}
	RecycleTTResponse(resp)
	if resp.Job != nil || resp.ForceCleanUp || resp.Err != nil {
		t.Fatalf("recycled TTResponse not reset: %+v", *resp)
	}

	rr := GetReadRequest(9, job, &msgID, 2, 5)
	if rr.CmdID != 9 || rr.Job != job || rr.MsgID != &msgID || rr.N != 2 || rr.Reqs != 5 {
		t.Fatalf("GetReadRequest fields = %+v", *rr)
	}
	rd2 := GetResponseData(9, 239, "239 <a@b>", nil)
	rr.ClearReadRequest(rd2)
	if rr.CmdID != 0 || rr.Job != nil || rr.MsgID != nil || rr.N != 0 || rr.Reqs != 0 {
		t.Fatalf("cleared ReadRequest not reset: %+v", *rr)
	}
	if rd2.CmdID != 0 || rd2.Code != 0 || rd2.Line != "" || rd2.Err != nil {
		t.Fatalf("ResponseData recycled by ClearReadRequest not reset: %+v", *rd2)
	}

	rc := GetTTResponseChan()
	if rc == nil || cap(rc) != 1 {
		t.Fatalf("GetTTResponseChan: cap = %d, want 1", cap(rc))
	}
	// a cap-1 chan holding a value is drained on recycle
	full := make(chan *TTResponse, 1)
	full <- &TTResponse{}
	RecycleTTResponseChan(full)
	if len(full) != 0 {
		t.Fatalf("RecycleTTResponseChan did not drain a cap-1 chan: len = %d", len(full))
	}
	// a chan of the wrong capacity is rejected (left untouched, not drained)
	wrong := make(chan *TTResponse, 2)
	wrong <- &TTResponse{}
	RecycleTTResponseChan(wrong)
	if len(wrong) != 1 {
		t.Fatalf("RecycleTTResponseChan touched a cap-2 chan: len = %d", len(wrong))
	}
	RecycleTTResponseChan(rc)
}

// TestCliTxDemuxerDepth: NewResponseDemuxer sizes its three channels with
// DemuxerQueueDepth (A-nntp-client-6). The package default is read, never mutated (K3).
func TestCliTxDemuxerDepth(t *testing.T) {
	want := DemuxerQueueDepth
	if want != 1<<20 {
		t.Fatalf("DemuxerQueueDepth default = %d, want %d", want, 1<<20)
	}
	d := NewResponseDemuxer(nil, make(chan struct{}, 1))
	if cap(d.cmdIDChan) != want || cap(d.checkResponseChan) != want || cap(d.ttResponseChan) != want {
		t.Fatalf("demuxer channel caps = %d/%d/%d, want %d", cap(d.cmdIDChan), cap(d.checkResponseChan), cap(d.ttResponseChan), want)
	}
}
