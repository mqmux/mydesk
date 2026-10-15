package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func streamTestTuning(t *testing.T, target int) {
	t.Helper()
	old := activeServerTuning
	t.Cleanup(func() { activeServerTuning = old })
	activeServerTuning.TargetKBps = target
	if target >= 4096 {
		activeServerTuning.MaxAutoFPS = 60
		activeServerTuning.ControllerWSFPS = 60
	}
}

func TestTCPQualitySurvivesCongestion(t *testing.T) {
	streamTestTuning(t, 500)
	now := time.Now()
	for _, requested := range []int{0, 80, 100} {
		sub := &frameSubscriber{Transport: "ws", PreferredQuality: requested}
		_, _, healthyFPS, healthyQuality, _ := subscriberProfileLocked(sub, now)
		sub.SlowScore = 6
		sub.LastResyncAt = now
		sub.RecentBPS = 1000 * 1024
		_, _, fps, quality, _ := subscriberProfileLocked(sub, now)
		if quality != healthyQuality || fps >= healthyFPS {
			t.Fatalf("congestion must lower cadence, not quality: request=%d fps=%d/%d quality=%d/%d", requested, fps, healthyFPS, quality, healthyQuality)
		}
	}
}

func TestSharedBandwidthBudget(t *testing.T) {
	streamTestTuning(t, 32768)
	fast := &frameSubscriber{Transport: "ws"}
	slow := &frameSubscriber{Transport: "ws", PreferredTargetKBps: 500}
	dev := &deviceState{Subscribers: map[*frameSubscriber]struct{}{fast: {}, slow: {}}}
	h := &hub{}
	resp := h.buildCommandResponseLocked(dev, 0, nil)
	if resp.SuggestedTargetKBps != 500 {
		t.Fatalf("shared budget = %d", resp.SuggestedTargetKBps)
	}
	slow.Paused = true
	resp = h.buildCommandResponseLocked(dev, 0, nil)
	if resp.SuggestedTargetKBps != 32768 || resp.SuggestedFPS != 60 {
		t.Fatalf("LAN profile: %+v", resp)
	}
	fast.Paused = true
	resp = h.buildCommandResponseLocked(dev, 0, nil)
	if resp.ViewerActive || resp.SuggestedTargetKBps != 0 {
		t.Fatalf("paused profile: %+v", resp)
	}
}

func TestBandwidthPreferenceAndAuto(t *testing.T) {
	streamTestTuning(t, 500)
	sub := &frameSubscriber{}
	applyControllerPreferencesLocked(sub, wsClientMessage{TargetKBps: 32768})
	if got := resolveTargetKBpsLocked(sub, time.Now(), 60); got != 32768 {
		t.Fatalf("LAN budget clamped: %d", got)
	}
	applyControllerPreferencesLocked(sub, wsClientMessage{})
	if got := resolveTargetKBpsLocked(sub, time.Now(), 60); got != 500 {
		t.Fatalf("default budget: %d", got)
	}
	activeServerTuning.TargetKBps = 0
	sub = &frameSubscriber{}
	resolveTargetKBpsLocked(sub, time.Now(), 60)
	if got := subscriberTargetKBps(sub); got != 500 {
		t.Fatalf("auto initial budget: %d", got)
	}
}

func TestTCPWindowBoundaries(t *testing.T) {
	streamTestTuning(t, 32768)
	now := time.Now()
	sub := &frameSubscriber{Transport: "ws", AvgFrameBytes: 100 * 1024}
	// IDs deliberately skip: two sent frames must not count as 200 queued frames.
	noteFrameSentLocked(sub, 100, 100*1024, now)
	noteFrameSentLocked(sub, 200, 100*1024, now)
	_, _, fps, _, _ := subscriberProfileLocked(sub, now)
	if fps != 60 || !canPipelineTCP(sub, now) {
		t.Fatalf("fresh pipeline unnecessarily throttled: fps=%d", fps)
	}
	noteFrameSentLocked(sub, 201, 100*1024, now)
	noteFrameSentLocked(sub, 202, 100*1024, now)
	if canPipelineTCP(sub, now) {
		t.Fatal("more than four unacknowledged frames allowed")
	}
	ackFramesLocked(sub, 200, now)
	if len(sub.InFlight) != 2 || !canPipelineTCP(sub, now) {
		t.Fatal("cumulative ACK did not release window")
	}
	if canPipelineTCP(sub, now.Add(151*time.Millisecond)) {
		t.Fatal("stale pipeline was allowed to grow")
	}
	sub.CurrentTargetKBps = 4096
	sub.InFlight[0].Bytes = 512 * 1024
	if canPipelineTCP(sub, now) {
		t.Fatal("byte budget ignored")
	}
	ackFramesLocked(sub, 202, now)
	sub.SlowScore = 3
	if !canPipelineTCP(sub, now) {
		t.Fatal("empty window cannot recover from resync")
	}
	// ACK can precede post-write bookkeeping on a very fast LAN.
	ackFramesLocked(sub, 203, now)
	noteFrameSentLocked(sub, 203, 100, now)
	if len(sub.InFlight) != 0 {
		t.Fatal("early ACK left phantom frame in window")
	}
}

func TestCloudUsesActualOutstandingFrames(t *testing.T) {
	streamTestTuning(t, 500)
	now := time.Now()
	sub := &frameSubscriber{Transport: "ws", LastSentFrameID: 12, AckFrameID: 10, LastSentAt: now, LastAckAt: now}
	_, _, fps, _, _ := subscriberProfileLocked(sub, now)
	if fps != activeServerTuning.ControllerWSFPS || highBandwidthTCP(sub) || !shouldHoldFullFallback(sub) {
		t.Fatalf("cloud policy changed: fps=%d", fps)
	}
	for _, id := range []int64{100, 200, 300} {
		noteFrameSentLocked(sub, id, 2048, now)
	}
	if !canPipelineIncrementalLocked(sub) {
		t.Fatal("small cloud frames blocked by skipped IDs")
	}
	_, _, fps, _, _ = subscriberProfileLocked(sub, now)
	if fps != activeServerTuning.ControllerWSFPS {
		t.Fatalf("fresh pipeline incorrectly lowers fps: %d", fps)
	}
	noteFrameSentLocked(sub, 400, 2048, now)
	if canPipelineIncrementalLocked(sub) {
		t.Fatal("cloud frame window unbounded")
	}
	ackFramesLocked(sub, 400, now)
	noteFrameSentLocked(sub, 401, 100*1024, now)
	if canPipelineIncrementalLocked(sub) {
		t.Fatal("large cloud frame ignored byte budget")
	}
	sub.Transport = "quic"
	sub.PreferredTargetKBps = 32768
	if highBandwidthTCP(sub) {
		t.Fatal("TCP pipeline applied to datagrams")
	}
}

func TestAutoTargetProbesWithoutTreatingThroughputAsLoss(t *testing.T) {
	streamTestTuning(t, 0)
	now := time.Now()
	sub := &frameSubscriber{Transport: "ws", AvgFrameBytes: 16000}
	if got := resolveTargetKBpsLocked(sub, now, 36); got != 500 {
		t.Fatalf("initial estimate = %d", got)
	}
	sub.RecentBPS = 600 * 1024
	sub.LastAckAt = now.Add(3 * time.Second)
	if got := resolveTargetKBpsLocked(sub, now.Add(3*time.Second), 36); got <= 500 {
		t.Fatalf("healthy delivered throughput failed to raise estimate: %d", got)
	}
	before := sub.CurrentTargetKBps
	sub.SlowScore = 4
	if got := resolveTargetKBpsLocked(sub, now.Add(5*time.Second), 36); got >= before {
		t.Fatalf("congestion failed to reduce estimate: %d >= %d", got, before)
	}
}

func packetHeader(t *testing.T, packet *wsFramePacket) (wsFrameEnvelope, []byte) {
	t.Helper()
	if packet == nil {
		t.Fatal("expected frame")
	}
	n := int(binary.BigEndian.Uint32(packet.body[:4]))
	var header wsFrameEnvelope
	if err := json.Unmarshal(packet.body[4:4+n], &header); err != nil {
		t.Fatal(err)
	}
	return header, packet.body[4+n:]
}

func TestFullAndIncrementalTCPPipeline(t *testing.T) {
	streamTestTuning(t, 32768)
	sub := &frameSubscriber{Transport: "ws", Notify: make(chan struct{}, 1)}
	dev := &deviceState{DeviceID: "test", Width: 32, Height: 32, Subscribers: map[*frameSubscriber]struct{}{sub: {}}}
	h := &hub{devices: map[string]*deviceState{"test": dev}}
	full, err := encodeJPEG(image.NewRGBA(image.Rect(0, 0, 32, 32)), 60)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyFullFrameLocked(dev, full, "jpeg", 60); err != nil {
		t.Fatal(err)
	}
	packet, err := h.buildFramePacket("test", sub)
	if err != nil {
		t.Fatal(err)
	}
	packetHeader(t, packet)
	noteFrameSentLocked(sub, packet.frameID, len(packet.body), time.Now())
	// A second independent JPEG is sent without waiting for the first ACK.
	applyFullFrameLocked(dev, full, "jpeg", 60)
	packet, err = h.buildFramePacket("test", sub)
	if err != nil {
		t.Fatal(err)
	}
	header, payload := packetHeader(t, packet)
	if header.FrameID != 2 || !bytes.Equal(full, payload) {
		t.Fatal("new full frame not selected")
	}
	noteFrameSentLocked(sub, packet.frameID, len(packet.body), time.Now())
	patch, _ := encodeJPEG(image.NewRGBA(image.Rect(0, 0, 4, 4)), 60)
	if err := applyPatchLocked(dev, patch, "jpeg", 0, 0, 4, 4, 60); err != nil {
		t.Fatal(err)
	}
	packet, err = h.buildFramePacket("test", sub)
	if err != nil {
		t.Fatal(err)
	}
	header, payload = packetHeader(t, packet)
	if header.UpdateKind != "patch" || header.BaseFrameID != 2 || header.FrameID != 3 || !bytes.Equal(patch, payload) {
		t.Fatalf("broken patch chain: %+v", header)
	}
	noteFrameSentLocked(sub, packet.frameID, len(packet.body), time.Now())
	// A fresh full JPEG supersedes unsent old increments, then patches use its ID.
	applyPatchLocked(dev, patch, "jpeg", 0, 0, 4, 4, 60)
	applyFullFrameLocked(dev, full, "jpeg", 60)
	packet, err = h.buildFramePacket("test", sub)
	if err != nil {
		t.Fatal(err)
	}
	header, _ = packetHeader(t, packet)
	if header.FrameID != 5 || header.UpdateKind != "full" {
		t.Fatalf("stale patch preferred: %+v", header)
	}
	noteFrameSentLocked(sub, packet.frameID, len(packet.body), time.Now())
	applyFullFrameLocked(dev, full, "jpeg", 60)
	packet, err = h.buildFramePacket("test", sub)
	if err != nil || packet != nil {
		t.Fatal("full window did not stop frame pump")
	}
	h.handleControllerControlMessageLocked(dev, sub, wsClientMessage{Type: "resync"}, "test")
	packet, err = h.buildFramePacket("test", sub)
	if err != nil {
		t.Fatal(err)
	}
	header, _ = packetHeader(t, packet)
	if header.FrameID != 6 || header.UpdateKind != "full" {
		t.Fatal("resync failed")
	}
}

// Exercise the real TCP writer/reader and ACK handler, not just selection logic.
// Four complete JPEGs must arrive before the receiver sends its first ACK.
func TestLoopbackWebSocketPipeline(t *testing.T) {
	streamTestTuning(t, 32768)
	dev := &deviceState{DeviceID: "test", Width: 32, Height: 32}
	h := &hub{devices: map[string]*deviceState{"test": dev}}
	server := httptest.NewServer(http.HandlerFunc(h.handleControllerWS))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"?deviceId=test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	full, err := encodeJPEG(image.NewRGBA(image.Rect(0, 0, 32, 32)), 60)
	if err != nil {
		t.Fatal(err)
	}
	for id := int64(1); id <= 6; id++ {
		h.mu.Lock()
		err = applyFullFrameLocked(dev, full, "jpeg", 60)
		h.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("frame %d before ACK: %v", id, err)
		}
		header, payload := packetHeader(t, &wsFramePacket{body: data})
		if header.FrameID != id || !bytes.Equal(payload, full) {
			t.Fatalf("wrong frame %d: %+v", id, header)
		}
		if id >= 4 {
			if err := conn.WriteJSON(wsClientMessage{Type: "ack", FrameID: id}); err != nil {
				t.Fatal(err)
			}
		}
	}
}
