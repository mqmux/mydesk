package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"io"
	"log"
	"math/big"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	quic "github.com/quic-go/quic-go"
)

type inputEvent struct {
	Seq           int64           `json:"seq,omitempty"`
	DeviceID      string          `json:"deviceId"`
	Type          string          `json:"type"`
	X             int             `json:"x,omitempty"`
	Y             int             `json:"y,omitempty"`
	Button        string          `json:"button,omitempty"`
	Delta         int             `json:"delta,omitempty"`
	Key           string          `json:"key,omitempty"`
	Code          string          `json:"code,omitempty"`
	Text          string          `json:"text,omitempty"`
	VK            int             `json:"vk,omitempty"`
	Extended      bool            `json:"extended,omitempty"`
	Ctrl          bool            `json:"ctrl,omitempty"`
	Alt           bool            `json:"alt,omitempty"`
	Shift         bool            `json:"shift,omitempty"`
	Meta          bool            `json:"meta,omitempty"`
	Repeat        bool            `json:"repeat,omitempty"`
	Clipboard     *clipboardState `json:"clipboard,omitempty"`
	ClipboardSync *bool           `json:"clipboardSync,omitempty"`
}

type transferFileRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type transferCommandPayload struct {
	Kind   string            `json:"kind"`
	Target string            `json:"target,omitempty"`
	Files  []transferFileRef `json:"files"`
}

type transferCreateRequest struct {
	DeviceID  string `json:"deviceId"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Direction string `json:"direction"`
}

type transferProgressRequest struct {
	ID             string `json:"id"`
	DeliveredBytes int64  `json:"deliveredBytes"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
}

type transferSnapshot struct {
	ID             string `json:"id"`
	DeviceID       string `json:"deviceId"`
	Name           string `json:"name"`
	Direction      string `json:"direction"`
	Size           int64  `json:"size"`
	UploadedBytes  int64  `json:"uploadedBytes"`
	DeliveredBytes int64  `json:"deliveredBytes"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
	UpdatedAt      string `json:"updatedAt"`
}

type remoteFileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime,omitempty"`
}

type remoteFileResult struct {
	ID        string            `json:"id"`
	DeviceID  string            `json:"deviceId"`
	Path      string            `json:"path,omitempty"`
	Parent    string            `json:"parent,omitempty"`
	Entries   []remoteFileEntry `json:"entries,omitempty"`
	File      *transferFileRef  `json:"file,omitempty"`
	Error     string            `json:"error,omitempty"`
	UpdatedAt string            `json:"updatedAt,omitempty"`
}

type remoteCommandResult struct {
	ID         string `json:"id"`
	DeviceID   string `json:"deviceId"`
	Command    string `json:"command,omitempty"`
	Dir        string `json:"dir,omitempty"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	ExitCode   int    `json:"exitCode,omitempty"`
	TimedOut   bool   `json:"timedOut,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	UpdatedAt  string `json:"updatedAt,omitempty"`
}

type transferState struct {
	ID             string
	DeviceID       string
	Name           string
	Direction      string
	TempPath       string
	Size           int64
	UploadedBytes  int64
	DeliveredBytes int64
	UploadDone     bool
	Status         string
	Error          string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type registerRequest struct {
	DeviceID         string `json:"deviceId"`
	Name             string `json:"name"`
	OriginX          int    `json:"originX"`
	OriginY          int    `json:"originY"`
	Width            int    `json:"width"`
	Height           int    `json:"height"`
	CursorX          int    `json:"cursorX"`
	CursorY          int    `json:"cursorY"`
	CursorOn         bool   `json:"cursorVisible"`
	CursorKind       string `json:"cursorKind"`
	CursorImage      []byte `json:"cursorImage,omitempty"`
	CursorImageClear bool   `json:"cursorImageClear,omitempty"`
	CursorImageW     int    `json:"cursorImageW,omitempty"`
	CursorImageH     int    `json:"cursorImageH,omitempty"`
	CursorHotX       int    `json:"cursorHotX,omitempty"`
	CursorHotY       int    `json:"cursorHotY,omitempty"`
}

type deviceAliasRequest struct {
	DeviceID string `json:"deviceId"`
	Alias    string `json:"alias"`
}

type tilePatch struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	W      int `json:"w"`
	H      int `json:"h"`
	AtlasX int `json:"atlasX"`
	AtlasY int `json:"atlasY"`
}

type deviceState struct {
	DeviceID              string                        `json:"deviceId"`
	Name                  string                        `json:"name"`
	Alias                 string                        `json:"alias,omitempty"`
	OriginX               int                           `json:"originX"`
	OriginY               int                           `json:"originY"`
	Width                 int                           `json:"width"`
	Height                int                           `json:"height"`
	FrameID               int64                         `json:"frameId"`
	UpdateKind            string                        `json:"updateKind"`
	DesktopStatus         string                        `json:"desktopStatus,omitempty"`
	DesktopMessage        string                        `json:"desktopMessage,omitempty"`
	DesktopUpdated        string                        `json:"desktopUpdated,omitempty"`
	HasPatch              bool                          `json:"hasPatch"`
	PatchFrameID          int64                         `json:"patchFrameId"`
	PatchBaseFrameID      int64                         `json:"patchBaseFrameId"`
	PatchX                int                           `json:"patchX"`
	PatchY                int                           `json:"patchY"`
	PatchW                int                           `json:"patchW"`
	PatchH                int                           `json:"patchH"`
	HasTiles              bool                          `json:"hasTiles"`
	TilesFrameID          int64                         `json:"tilesFrameId"`
	TilesBaseFrameID      int64                         `json:"tilesBaseFrameId"`
	Tiles                 []tilePatch                   `json:"tiles,omitempty"`
	Online                bool                          `json:"online"`
	LastSeen              string                        `json:"lastSeen"`
	UpdatedAt             string                        `json:"updatedAt"`
	HasFrame              bool                          `json:"hasFrame"`
	Clipboard             string                        `json:"clipboard,omitempty"`
	ClipboardVersion      int64                         `json:"clipboardVersion"`
	ClipboardUpdated      string                        `json:"clipboardUpdated"`
	Commands              int                           `json:"commands"`
	CursorX               int                           `json:"cursorX"`
	CursorY               int                           `json:"cursorY"`
	CursorVisible         bool                          `json:"cursorVisible"`
	CursorKind            string                        `json:"cursorKind"`
	CursorImage           []byte                        `json:"-"`
	CursorImageW          int                           `json:"-"`
	CursorImageH          int                           `json:"-"`
	CursorHotX            int                           `json:"-"`
	CursorHotY            int                           `json:"-"`
	CursorVersion         int64                         `json:"-"`
	FrameCodec            string                        `json:"-"`
	FrameQuality          int                           `json:"-"`
	Frame                 []byte                        `json:"-"`
	FullFrameID           int64                         `json:"-"`
	Patch                 []byte                        `json:"-"`
	TilesAtlas            []byte                        `json:"-"`
	Image                 *image.RGBA                   `json:"-"`
	Queue                 []inputEvent                  `json:"-"`
	NextSeq               int64                         `json:"-"`
	ClipboardText         string                        `json:"-"`
	ClipboardKind         string                        `json:"-"`
	ClipboardImage        []byte                        `json:"-"`
	ClipboardImageType    string                        `json:"-"`
	ClipboardImageW       int                           `json:"-"`
	ClipboardImageH       int                           `json:"-"`
	ClipboardFiles        []transferFileRef             `json:"-"`
	ClipboardAt           time.Time                     `json:"-"`
	ClipboardSyncDisabled bool                          `json:"-"`
	SeenAt                time.Time                     `json:"-"`
	FrameAt               time.Time                     `json:"-"`
	DesktopAt             time.Time                     `json:"-"`
	AgentSessionCount     int                           `json:"-"`
	AgentSessionKnown     bool                          `json:"-"`
	AgentSessionEnded     time.Time                     `json:"-"`
	Notify                chan struct{}                 `json:"-"`
	ViewerKind            string                        `json:"-"`
	ViewerAt              time.Time                     `json:"-"`
	Subscribers           map[*frameSubscriber]struct{} `json:"-"`
	UploadStatAt          time.Time                     `json:"-"`
	UploadStatBytes       int64                         `json:"-"`
	UploadStatFrames      int                           `json:"-"`
	UploadStatLast        string                        `json:"-"`
	UploadFPS             float64                       `json:"-"`
	UploadBPS             float64                       `json:"-"`
	UploadAvgBytes        float64                       `json:"-"`
	RecentUpdates         []recentFrameUpdate           `json:"-"`
}

type recentFrameUpdate struct {
	FrameID     int64
	UpdateKind  string
	BaseFrameID int64
	Codec       string
	Quality     int
	Payload     []byte
	PatchX      int
	PatchY      int
	PatchW      int
	PatchH      int
	Tiles       []tilePatch
}

type commandResponse struct {
	LastSeq          int64        `json:"lastSeq"`
	Commands         []inputEvent `json:"commands"`
	ViewerActive     bool         `json:"viewerActive"`
	ViewerKind       string       `json:"viewerKind,omitempty"`
	SuggestedMode    string       `json:"suggestedMode,omitempty"`
	SuggestedCodec   string       `json:"suggestedCodec,omitempty"`
	SuggestedFPS     int          `json:"suggestedFps,omitempty"`
	SuggestedQuality int          `json:"suggestedQuality,omitempty"`
	SuggestedFullMS  int          `json:"suggestedFullRefreshMs,omitempty"`
	ClipboardSync    *bool        `json:"clipboardSync,omitempty"`
}

type frameSubscriber struct {
	Notify              chan struct{}
	Done                chan struct{}
	Transport           string
	PreferredCodec      string
	PreferredMode       string
	PreferredFPS        int
	PreferredQuality    int
	PreferredFullMS     int
	PreferredTargetKBps int
	Paused              bool
	AckFrameID          int64
	LastSentFrameID     int64
	LastSentCursorVer   int64
	LastDesktopStatus   string
	LastAckAt           time.Time
	LastSentAt          time.Time
	LastResyncAt        time.Time
	LastPingAt          time.Time
	SlowScore           int
	CurrentTargetKBps   int
	AdaptiveTargetKBps  int
	AdaptiveTuneAt      time.Time
	LogStatAt           time.Time
	LogStatBytes        int64
	LogStatFrames       int
	LogStatLast         string
	RecentFPS           float64
	RecentBPS           float64
	AvgFrameBytes       float64
}

type wsFrameEnvelope struct {
	Type        string      `json:"type"`
	DeviceID    string      `json:"deviceId"`
	Name        string      `json:"name,omitempty"`
	Alias       string      `json:"alias,omitempty"`
	FrameID     int64       `json:"frameId"`
	UpdateKind  string      `json:"updateKind"`
	Status      string      `json:"status,omitempty"`
	Message     string      `json:"message,omitempty"`
	BaseFrameID int64       `json:"baseFrameId,omitempty"`
	Width       int         `json:"width"`
	Height      int         `json:"height"`
	OriginX     int         `json:"originX"`
	OriginY     int         `json:"originY"`
	CursorX     int         `json:"cursorX"`
	CursorY     int         `json:"cursorY"`
	CursorOn    bool        `json:"cursorVisible"`
	CursorKind  string      `json:"cursorKind"`
	CursorImage []byte      `json:"cursorImage,omitempty"`
	CursorClear bool        `json:"cursorImageClear,omitempty"`
	CursorImgW  int         `json:"cursorImageW,omitempty"`
	CursorImgH  int         `json:"cursorImageH,omitempty"`
	CursorHotX  int         `json:"cursorHotX,omitempty"`
	CursorHotY  int         `json:"cursorHotY,omitempty"`
	CursorVer   int64       `json:"cursorVersion,omitempty"`
	Codec       string      `json:"codec,omitempty"`
	Quality     int         `json:"quality,omitempty"`
	PatchX      int         `json:"patchX,omitempty"`
	PatchY      int         `json:"patchY,omitempty"`
	PatchW      int         `json:"patchW,omitempty"`
	PatchH      int         `json:"patchH,omitempty"`
	Tiles       []tilePatch `json:"tiles,omitempty"`
	PayloadSize int         `json:"payloadSize"`
}

type wsClientMessage struct {
	Type       string      `json:"type"`
	FrameID    int64       `json:"frameId,omitempty"`
	DeviceID   string      `json:"deviceId,omitempty"`
	Key        string      `json:"key,omitempty"`
	Role       string      `json:"role,omitempty"`
	Codec      string      `json:"codec,omitempty"`
	Mode       string      `json:"mode,omitempty"`
	FPS        int         `json:"fps,omitempty"`
	Quality    int         `json:"quality,omitempty"`
	FullMS     int         `json:"fullMs,omitempty"`
	TargetKBps int         `json:"targetKBps,omitempty"`
	Paused     *bool       `json:"paused,omitempty"`
	Input      *inputEvent `json:"input,omitempty"`
}

type agentPollRequest struct {
	DeviceID         string `json:"deviceId,omitempty"`
	After            int64  `json:"after"`
	CursorX          int    `json:"cursorX"`
	CursorY          int    `json:"cursorY"`
	CursorVisible    bool   `json:"cursorVisible"`
	CursorKind       string `json:"cursorKind"`
	CursorImage      []byte `json:"cursorImage,omitempty"`
	CursorImageClear bool   `json:"cursorImageClear,omitempty"`
	CursorImageW     int    `json:"cursorImageW,omitempty"`
	CursorImageH     int    `json:"cursorImageH,omitempty"`
	CursorHotX       int    `json:"cursorHotX,omitempty"`
	CursorHotY       int    `json:"cursorHotY,omitempty"`
}

type agentStreamMessage struct {
	Type      string            `json:"type"`
	DeviceID  string            `json:"deviceId,omitempty"`
	Role      string            `json:"role,omitempty"`
	Register  *registerRequest  `json:"register,omitempty"`
	Clipboard *clipboardState   `json:"clipboard,omitempty"`
	Poll      *agentPollRequest `json:"poll,omitempty"`
	Commands  *commandResponse  `json:"commands,omitempty"`
	Error     string            `json:"error,omitempty"`
}

type agentFrameEnvelope struct {
	Type        string      `json:"type"`
	DeviceID    string      `json:"deviceId"`
	Mode        string      `json:"mode"`
	Status      string      `json:"status,omitempty"`
	Message     string      `json:"message,omitempty"`
	Codec       string      `json:"codec,omitempty"`
	OriginX     int         `json:"originX"`
	OriginY     int         `json:"originY"`
	Width       int         `json:"width"`
	Height      int         `json:"height"`
	Quality     int         `json:"quality"`
	PatchX      int         `json:"patchX,omitempty"`
	PatchY      int         `json:"patchY,omitempty"`
	PatchW      int         `json:"patchW,omitempty"`
	PatchH      int         `json:"patchH,omitempty"`
	Tiles       []tilePatch `json:"tiles,omitempty"`
	PayloadSize int         `json:"payloadSize"`
}

type clipboardState struct {
	DeviceID  string            `json:"deviceId"`
	Kind      string            `json:"kind,omitempty"`
	Text      string            `json:"text,omitempty"`
	Image     []byte            `json:"image,omitempty"`
	ImageType string            `json:"imageType,omitempty"`
	ImageW    int               `json:"imageW,omitempty"`
	ImageH    int               `json:"imageH,omitempty"`
	Files     []transferFileRef `json:"files,omitempty"`
	Version   int64             `json:"version"`
	UpdatedAt string            `json:"updatedAt"`
	Sync      *bool             `json:"sync,omitempty"`
}

type hub struct {
	mu                sync.Mutex
	devices           map[string]*deviceState
	transfers         map[string]*transferState
	fileResults       map[string]remoteFileResult
	commandResults    map[string]remoteCommandResult
	adminKey          string
	offlinePruneAfter time.Duration
	offlinePruneEvery time.Duration
	transferDir       string
}

type serverTuning struct {
	ControllerQUICFPS     int
	ControllerQUICQuality int
	ControllerQUICFullMS  int
	ControllerQUICMode    string
	ControllerWSFPS       int
	ControllerWSQuality   int
	ControllerWSFullMS    int
	ControllerWSMode      string
	WebFPS                int
	WebQuality            int
	WebFullMS             int
	WebMode               string
	TargetKBps            int
	AutoTargetMinKBps     int
	AutoTargetMaxKBps     int
	MaxAutoFPS            int
}

type wsFramePacket struct {
	frameID       int64
	body          []byte
	updateKind    string
	cursorVersion int64
	cursorOnly    bool
	statusOnly    bool
	statusKey     string
}

var activeServerTuning = serverTuning{
	ControllerQUICFPS:     54,
	ControllerQUICQuality: 60,
	ControllerQUICFullMS:  3200,
	ControllerQUICMode:    "video",
	ControllerWSFPS:       36,
	ControllerWSQuality:   56,
	ControllerWSFullMS:    5200,
	ControllerWSMode:      "video",
	WebFPS:                2,
	WebQuality:            58,
	WebFullMS:             9000,
	WebMode:               "delta",
	TargetKBps:            500,
	AutoTargetMinKBps:     256,
	AutoTargetMaxKBps:     1536,
	MaxAutoFPS:            48,
}

var activeRequestLogging = false
var activeTrafficLogWindow = 2 * time.Second

var controllerUpgrader = websocket.Upgrader{
	ReadBufferSize:    8 << 10,
	WriteBufferSize:   16 << 10,
	EnableCompression: false,
	CheckOrigin: func(_ *http.Request) bool {
		return true
	},
}

const (
	quicNextProto                  = "remote-desktop-quic-v1"
	quicDatagramMagicDownlink byte = 0x51
	quicDatagramMagicUplink   byte = 0x52
	quicDatagramHeaderSize         = 13
	quicDatagramChunkPayload       = 1120
	maxClipboardImageBytes         = 24 << 20
	maxClipboardJSONBytes          = 40 << 20
)

func newHub(adminKey string, offlinePruneAfter, offlinePruneEvery time.Duration) *hub {
	transferDir := filepath.Join(os.TempDir(), "go-remote-transfer-spool")
	if err := os.MkdirAll(transferDir, 0o755); err != nil {
		panic("failed to create transfer spool directory " + transferDir + ": " + err.Error())
	}
	return &hub{
		devices:           make(map[string]*deviceState),
		transfers:         make(map[string]*transferState),
		fileResults:       make(map[string]remoteFileResult),
		commandResults:    make(map[string]remoteCommandResult),
		adminKey:          adminKey,
		offlinePruneAfter: offlinePruneAfter,
		offlinePruneEvery: offlinePruneEvery,
		transferDir:       transferDir,
	}
}

func (h *hub) startOfflinePruner() {
	if h == nil || h.offlinePruneAfter <= 0 || h.offlinePruneEvery <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(h.offlinePruneEvery)
		defer ticker.Stop()
		for range ticker.C {
			h.pruneOfflineDevices()
		}
	}()
}

func (h *hub) pruneOfflineDevices() {
	if h == nil || h.offlinePruneAfter <= 0 {
		return
	}
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneOfflineDevicesLocked(now)
}

func (h *hub) pruneOfflineDevicesLocked(now time.Time) {
	if h == nil || h.offlinePruneAfter <= 0 {
		return
	}
	for deviceID, dev := range h.devices {
		if dev == nil || deviceOnlineLocked(dev, now) {
			continue
		}
		lastActive := deviceLastActiveLocked(dev)
		if lastActive.IsZero() || now.Sub(lastActive) < h.offlinePruneAfter {
			continue
		}
		delete(h.devices, deviceID)
		log.Printf("[srv][device] pruned offline device=%s name=%s lastActive=%s", deviceID, strings.TrimSpace(dev.Name), formatTime(lastActive))
	}
}

func deviceLastActiveLocked(dev *deviceState) time.Time {
	if dev == nil {
		return time.Time{}
	}
	lastActive := dev.SeenAt
	if dev.AgentSessionKnown && !dev.AgentSessionEnded.IsZero() && dev.AgentSessionEnded.After(lastActive) {
		lastActive = dev.AgentSessionEnded
	}
	return lastActive
}

func main() {
	addr := flag.String("addr", ":19998", "listen address")
	adminKey := flag.String("admin-key", "", "admin key for listing and controlling devices")
	targetKBps := flag.Int("target-kbps", activeServerTuning.TargetKBps, "adaptive controller downlink target in KB/s (0 = auto)")
	autoMinKBps := flag.Int("auto-min-kbps", activeServerTuning.AutoTargetMinKBps, "automatic controller downlink minimum in KB/s when target-kbps=0")
	autoMaxKBps := flag.Int("auto-max-kbps", activeServerTuning.AutoTargetMaxKBps, "automatic controller downlink maximum in KB/s when target-kbps=0")
	maxAutoFPS := flag.Int("max-fps", activeServerTuning.MaxAutoFPS, "adaptive controller fps ceiling")
	quicFPS := flag.Int("quic-fps", activeServerTuning.ControllerQUICFPS, "controller quic base fps")
	quicQuality := flag.Int("quic-quality", activeServerTuning.ControllerQUICQuality, "controller quic base jpeg quality")
	quicFullMS := flag.Int("quic-full-ms", activeServerTuning.ControllerQUICFullMS, "controller quic full-frame refresh interval in ms")
	quicMode := flag.String("quic-mode", activeServerTuning.ControllerQUICMode, "controller quic stream mode: video or delta")
	wsFPS := flag.Int("ws-fps", activeServerTuning.ControllerWSFPS, "controller websocket base fps")
	wsQuality := flag.Int("ws-quality", activeServerTuning.ControllerWSQuality, "controller websocket base jpeg quality")
	wsFullMS := flag.Int("ws-full-ms", activeServerTuning.ControllerWSFullMS, "controller websocket full-frame refresh interval in ms")
	wsMode := flag.String("ws-mode", activeServerTuning.ControllerWSMode, "controller websocket stream mode: video or delta")
	webFPS := flag.Int("web-fps", activeServerTuning.WebFPS, "web viewer base fps suggestion")
	webQuality := flag.Int("web-quality", activeServerTuning.WebQuality, "web viewer base jpeg quality suggestion")
	webFullMS := flag.Int("web-full-ms", activeServerTuning.WebFullMS, "web viewer full-frame refresh interval in ms")
	webMode := flag.String("web-mode", activeServerTuning.WebMode, "web viewer stream mode: video or delta")
	logRequestsFlag := flag.Bool("log-requests", false, "log every raw http request")
	logTrafficMS := flag.Int("log-traffic-ms", 2000, "traffic summary log window in ms")
	offlinePruneMinutes := flag.Int("offline-prune-minutes", 60, "delete offline device records after this many minutes (0 = disable)")
	offlinePruneScanMinutes := flag.Int("offline-prune-scan-minutes", 5, "scan interval for deleting old offline device records in minutes")
	flag.Parse()

	fixedTargetKBps := 0
	if *targetKBps > 0 {
		fixedTargetKBps = clamp(*targetKBps, 128, 4096)
	}
	minAutoKBps := clamp(*autoMinKBps, 128, 4096)
	maxAutoKBps := clamp(*autoMaxKBps, 128, 4096)
	if minAutoKBps > maxAutoKBps {
		minAutoKBps, maxAutoKBps = maxAutoKBps, minAutoKBps
	}

	activeServerTuning = serverTuning{
		ControllerQUICFPS:     clamp(*quicFPS, 1, 60),
		ControllerQUICQuality: clamp(*quicQuality, 30, 100),
		ControllerQUICFullMS:  clamp(*quicFullMS, 500, 15000),
		ControllerQUICMode:    normalizeStreamMode(*quicMode, "video"),
		ControllerWSFPS:       clamp(*wsFPS, 1, 60),
		ControllerWSQuality:   clamp(*wsQuality, 30, 100),
		ControllerWSFullMS:    clamp(*wsFullMS, 500, 15000),
		ControllerWSMode:      normalizeStreamMode(*wsMode, "delta"),
		WebFPS:                clamp(*webFPS, 1, 15),
		WebQuality:            clamp(*webQuality, 30, 100),
		WebFullMS:             clamp(*webFullMS, 1000, 20000),
		WebMode:               normalizeStreamMode(*webMode, "delta"),
		TargetKBps:            fixedTargetKBps,
		AutoTargetMinKBps:     minAutoKBps,
		AutoTargetMaxKBps:     maxAutoKBps,
		MaxAutoFPS:            clamp(*maxAutoFPS, 12, 60),
	}
	activeRequestLogging = *logRequestsFlag
	activeTrafficLogWindow = time.Duration(clamp(*logTrafficMS, 200, 20000)) * time.Millisecond

	key := strings.TrimSpace(*adminKey)
	if key == "" {
		key = randomKey(16)
	}

	pruneAfter := time.Duration(clamp(*offlinePruneMinutes, 0, 60*24*30)) * time.Minute
	pruneScan := time.Duration(clamp(*offlinePruneScanMinutes, 1, 60*24)) * time.Minute

	h := newHub(key, pruneAfter, pruneScan)
	h.startOfflinePruner()
	mux := http.NewServeMux()

	mux.HandleFunc("/", requireMethod(http.MethodGet, handleRoot))
	mux.HandleFunc("/healthz", requireMethod(http.MethodGet, handleHealth))
	mux.HandleFunc("/api/devices", methodSwitch(map[string]http.HandlerFunc{
		http.MethodGet:  h.handleDevices,
		http.MethodPost: h.handleDeviceAlias,
	}))
	mux.HandleFunc("/api/register", requireMethod(http.MethodPost, h.handleRegister))
	mux.HandleFunc("/api/frame", requireMethod(http.MethodPost, h.handleFrameUpload))
	mux.HandleFunc("/api/desktop-status", requireMethod(http.MethodPost, h.handleDesktopStatus))
	mux.HandleFunc("/api/frame.jpg", requireMethod(http.MethodGet, h.handleFrameImage))
	mux.HandleFunc("/api/patch.jpg", requireMethod(http.MethodGet, h.handlePatchImage))
	mux.HandleFunc("/api/tiles.jpg", requireMethod(http.MethodGet, h.handleTilesImage))
	mux.HandleFunc("/api/state", requireMethod(http.MethodGet, h.handleState))
	mux.HandleFunc("/api/state/wait", requireMethod(http.MethodGet, h.handleStateWait))
	mux.HandleFunc("/api/clipboard", methodSwitch(map[string]http.HandlerFunc{
		http.MethodGet:  h.handleClipboardGet,
		http.MethodPost: h.handleClipboardPost,
	}))
	mux.HandleFunc("/api/transfer/create", requireMethod(http.MethodPost, h.handleTransferCreate))
	mux.HandleFunc("/api/transfer/chunk", methodSwitch(map[string]http.HandlerFunc{
		http.MethodGet: h.handleTransferChunkGet,
		http.MethodPut: h.handleTransferChunkPut,
	}))
	mux.HandleFunc("/api/transfer/complete", requireMethod(http.MethodPost, h.handleTransferComplete))
	mux.HandleFunc("/api/transfer/progress", requireMethod(http.MethodPost, h.handleTransferProgress))
	mux.HandleFunc("/api/transfer/cancel", requireMethod(http.MethodPost, h.handleTransferCancel))
	mux.HandleFunc("/api/transfers", requireMethod(http.MethodGet, h.handleTransferList))
	mux.HandleFunc("/api/file/result", methodSwitch(map[string]http.HandlerFunc{
		http.MethodGet:  h.handleFileResultGet,
		http.MethodPost: h.handleFileResultPost,
	}))
	mux.HandleFunc("/api/command/result", methodSwitch(map[string]http.HandlerFunc{
		http.MethodGet:  h.handleCommandResultGet,
		http.MethodPost: h.handleCommandResultPost,
	}))
	mux.HandleFunc("/api/cursor", requireMethod(http.MethodPost, h.handleCursorUpdate))
	mux.HandleFunc("/api/input", requireMethod(http.MethodPost, h.handleInput))
	mux.HandleFunc("/api/commands", requireMethod(http.MethodGet, h.handleCommands))
	mux.HandleFunc("/ws/agent", requireMethod(http.MethodGet, h.handleAgentWS))
	mux.HandleFunc("/ws/controller", requireMethod(http.MethodGet, h.handleControllerWS))

	server := &http.Server{
		Addr:              *addr,
		Handler:           recoverHTTP(withCORS(logRequests(mux))),
		ReadHeaderTimeout: 5 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			configureServerTCP(c)
			return ctx
		},
	}

	log.Printf("server listening on %s", displayListenURL(*addr))
	go func() {
		if err := h.serveQUIC(*addr); err != nil {
			log.Printf("quic listener stopped: %v", err)
		}
	}()
	log.Printf("admin key: %s", key)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name":        "remote-desktop-server",
		"focus":       "controller-stream",
		"webViewer":   "deprecated",
		"health":      "ok",
		"targetKBps":  activeServerTuning.TargetKBps,
		"autoMinKBps": activeServerTuning.AutoTargetMinKBps,
		"autoMaxKBps": activeServerTuning.AutoTargetMaxKBps,
		"targetMode":  map[bool]string{true: "auto", false: "fixed"}[activeServerTuning.TargetKBps <= 0],
		"maxAutoFPS":  activeServerTuning.MaxAutoFPS,
		"streamModes": []string{"video", "delta"},
	})
}

func requireMethod(method string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next(w, r)
	}
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func methodSwitch(handlers map[string]http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if handler, ok := handlers[r.Method]; ok {
			handler(w, r)
			return
		}

		allowed := make([]string, 0, len(handlers))
		for method := range handlers {
			allowed = append(allowed, method)
		}
		sort.Strings(allowed)
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *hub) handleDevices(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdminKey(w, r) {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneOfflineDevicesLocked(time.Now())

	devices := make([]deviceState, 0, len(h.devices))
	for _, dev := range h.devices {
		devices = append(devices, h.snapshotLocked(dev))
	}

	sort.Slice(devices, func(i, j int) bool {
		return devices[i].DeviceID < devices[j].DeviceID
	})

	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

func (h *hub) handleDeviceAlias(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdminKey(w, r) {
		return
	}

	var req deviceAliasRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	deviceID := strings.TrimSpace(req.DeviceID)
	if deviceID == "" {
		http.Error(w, "deviceId is required", http.StatusBadRequest)
		return
	}
	alias := sanitizeDeviceAlias(req.Alias)

	h.mu.Lock()
	dev, ok := h.devices[deviceID]
	if !ok {
		h.mu.Unlock()
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}
	dev.Alias = alias
	notifyLocked(dev)
	snapshot := h.snapshotLocked(dev)
	h.mu.Unlock()

	writeJSON(w, http.StatusOK, snapshot)
}

func sanitizeDeviceAlias(alias string) string {
	alias = strings.Join(strings.Fields(strings.TrimSpace(alias)), " ")
	const maxAliasRunes = 64
	runes := []rune(alias)
	if len(runes) > maxAliasRunes {
		alias = string(runes[:maxAliasRunes])
	}
	return alias
}

func (h *hub) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024)).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	req.DeviceID = strings.TrimSpace(req.DeviceID)
	req.Name = strings.TrimSpace(req.Name)
	if req.DeviceID == "" {
		http.Error(w, "deviceId is required", http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	dev, wasOnline := h.applyRegisterLocked(req)
	if !wasOnline {
		log.Printf("[srv][agent] online device=%s name=%s desktop=%dx%d origin=(%d,%d)", dev.DeviceID, dev.Name, dev.Width, dev.Height, dev.OriginX, dev.OriginY)
	}

	writeJSON(w, http.StatusOK, h.snapshotLocked(dev))
}

func (h *hub) applyRegisterLocked(req registerRequest) (dev *deviceState, wasOnline bool) {
	dev = h.ensureDeviceLocked(req.DeviceID)
	wasOnline = deviceOnlineLocked(dev, time.Now())
	if req.Name != "" {
		dev.Name = req.Name
	}
	dev.OriginX = req.OriginX
	dev.OriginY = req.OriginY
	dev.Width = req.Width
	dev.Height = req.Height
	updateCursorLocked(dev, agentPollRequest{
		CursorX:       req.CursorX,
		CursorY:       req.CursorY,
		CursorVisible: req.CursorOn,
		CursorKind:    req.CursorKind,
	})
	dev.SeenAt = time.Now()
	if dev.DesktopStatus == "" {
		dev.DesktopStatus = "available"
	}
	notifyLocked(dev)
	return dev, wasOnline
}

func deviceOnlineLocked(dev *deviceState, now time.Time) bool {
	if dev == nil {
		return false
	}
	if dev.AgentSessionCount > 0 {
		return true
	}
	if dev.SeenAt.IsZero() || now.Sub(dev.SeenAt) >= 15*time.Second {
		return false
	}
	if dev.AgentSessionKnown && !dev.AgentSessionEnded.IsZero() && !dev.SeenAt.After(dev.AgentSessionEnded) {
		return false
	}
	return true
}

func (h *hub) setAgentSessionLocked(deviceID string, connected bool) {
	dev := h.ensureDeviceLocked(deviceID)
	wasOnline := deviceOnlineLocked(dev, time.Now())
	dev.AgentSessionKnown = true
	if connected {
		dev.AgentSessionCount++
		dev.AgentSessionEnded = time.Time{}
	} else if dev.AgentSessionCount > 0 {
		dev.AgentSessionCount--
		if dev.AgentSessionCount == 0 {
			dev.AgentSessionEnded = time.Now()
		}
	}
	nowOnline := deviceOnlineLocked(dev, time.Now())
	if wasOnline == nowOnline {
		return
	}
	if !nowOnline {
		log.Printf("[srv][agent] offline device=%s name=%s", dev.DeviceID, dev.Name)
	}
	notifyLocked(dev)
}

func (h *hub) applyClipboardLocked(payload clipboardState) clipboardState {
	dev := h.ensureDeviceLocked(payload.DeviceID)
	dev.SeenAt = time.Now()
	if applyClipboardToDeviceLocked(dev, payload) {
		notifyLocked(dev)
	}
	return clipboardSnapshotLocked(dev)
}

func applyClipboardToDeviceLocked(dev *deviceState, payload clipboardState) bool {
	if dev == nil {
		return false
	}
	if dev.ClipboardSyncDisabled {
		return false
	}
	if clipboardPayloadKind(payload) == "files" {
		return false
	}
	payload = normalizeClipboardPayload(payload)
	changed := dev.ClipboardKind != payload.Kind ||
		dev.ClipboardText != payload.Text ||
		dev.ClipboardImageType != payload.ImageType ||
		dev.ClipboardImageW != payload.ImageW ||
		dev.ClipboardImageH != payload.ImageH ||
		!bytes.Equal(dev.ClipboardImage, payload.Image) ||
		!sameTransferFileRefs(dev.ClipboardFiles, payload.Files)
	if changed {
		dev.ClipboardVersion++
		dev.ClipboardKind = payload.Kind
		dev.ClipboardText = payload.Text
		if len(payload.Image) > 0 {
			dev.ClipboardImage = append(dev.ClipboardImage[:0], payload.Image...)
		} else {
			dev.ClipboardImage = nil
		}
		dev.ClipboardImageType = payload.ImageType
		dev.ClipboardImageW = payload.ImageW
		dev.ClipboardImageH = payload.ImageH
		dev.ClipboardFiles = cloneTransferFileRefs(payload.Files)
		dev.ClipboardAt = time.Now()
	} else if dev.ClipboardAt.IsZero() {
		dev.ClipboardAt = time.Now()
	}
	return changed
}

func normalizeClipboardPayload(payload clipboardState) clipboardState {
	payload.Kind = strings.ToLower(strings.TrimSpace(payload.Kind))
	if payload.Kind == "" {
		if len(payload.Image) > 0 {
			payload.Kind = "image"
		} else {
			payload.Kind = "text"
		}
	}
	if payload.Kind == "image" {
		payload.Text = ""
		payload.Files = nil
		payload.ImageType = strings.ToLower(strings.TrimSpace(payload.ImageType))
		if payload.ImageType == "" {
			payload.ImageType = "png"
		}
		return payload
	}
	if payload.Kind == "files" {
		payload.Text = ""
		payload.Image = nil
		payload.ImageType = ""
		payload.ImageW = 0
		payload.ImageH = 0
		payload.Files = sanitizeTransferFileRefs(payload.Files)
		return payload
	}
	payload.Kind = "text"
	payload.Image = nil
	payload.ImageType = ""
	payload.ImageW = 0
	payload.ImageH = 0
	payload.Files = nil
	return payload
}

func clipboardPayloadKind(payload clipboardState) string {
	kind := strings.ToLower(strings.TrimSpace(payload.Kind))
	if kind != "" {
		return kind
	}
	if len(payload.Image) > 0 {
		return "image"
	}
	if len(payload.Files) > 0 {
		return "files"
	}
	return "text"
}

func inputClipboardKind(ev inputEvent) string {
	payload := clipboardState{Kind: "text", Text: ev.Text}
	if ev.Clipboard != nil {
		payload = *ev.Clipboard
	}
	return clipboardPayloadKind(payload)
}

func sanitizeTransferFileRefs(files []transferFileRef) []transferFileRef {
	if len(files) == 0 {
		return nil
	}
	out := make([]transferFileRef, 0, len(files))
	for _, file := range files {
		file.ID = strings.TrimSpace(file.ID)
		file.Name = strings.TrimSpace(file.Name)
		if file.ID == "" || file.Name == "" || file.Size < 0 {
			continue
		}
		out = append(out, file)
	}
	return out
}

func cloneTransferFileRefs(files []transferFileRef) []transferFileRef {
	if len(files) == 0 {
		return nil
	}
	return append([]transferFileRef(nil), files...)
}

func sameTransferFileRefs(a, b []transferFileRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func boolPtr(v bool) *bool {
	return &v
}

func clipboardSnapshotLocked(dev *deviceState) clipboardState {
	if dev == nil {
		return clipboardState{}
	}
	kind := dev.ClipboardKind
	if kind == "" {
		kind = "text"
	}
	return clipboardState{
		DeviceID:  dev.DeviceID,
		Kind:      kind,
		Text:      dev.ClipboardText,
		Image:     append([]byte(nil), dev.ClipboardImage...),
		ImageType: dev.ClipboardImageType,
		ImageW:    dev.ClipboardImageW,
		ImageH:    dev.ClipboardImageH,
		Files:     cloneTransferFileRefs(dev.ClipboardFiles),
		Version:   dev.ClipboardVersion,
		UpdatedAt: formatTime(dev.ClipboardAt),
		Sync:      boolPtr(!dev.ClipboardSyncDisabled),
	}
}

func clipboardCommandMatchesCurrentLocked(dev *deviceState, cmd inputEvent) bool {
	if dev == nil {
		return false
	}
	payload := clipboardState{Kind: "text", Text: cmd.Text}
	if cmd.Clipboard != nil {
		payload = *cmd.Clipboard
	}
	payload.DeviceID = dev.DeviceID
	payload = normalizeClipboardPayload(payload)
	current := normalizeClipboardPayload(clipboardSnapshotLocked(dev))
	return payload.Kind == current.Kind &&
		payload.Text == current.Text &&
		payload.ImageType == current.ImageType &&
		payload.ImageW == current.ImageW &&
		payload.ImageH == current.ImageH &&
		bytes.Equal(payload.Image, current.Image) &&
		sameTransferFileRefs(payload.Files, current.Files)
}

func (h *hub) buildCommandResponseLocked(dev *deviceState, after int64, cursor *agentPollRequest) commandResponse {
	if cursor != nil {
		updateCursorLocked(dev, *cursor)
	}
	if after > 0 {
		dev.Queue = trimAckedInputQueue(dev.Queue, after)
	}
	resp := commandResponse{
		LastSeq:       dev.NextSeq,
		ClipboardSync: boolPtr(!dev.ClipboardSyncDisabled),
	}
	if kind, mode, codec, fps, quality, fullMS, active := viewerProfileLocked(dev); active {
		resp.ViewerActive = true
		resp.ViewerKind = kind
		resp.SuggestedMode = mode
		resp.SuggestedCodec = codec
		resp.SuggestedFPS = fps
		resp.SuggestedQuality = quality
		resp.SuggestedFullMS = fullMS
	}
	for _, cmd := range dev.Queue {
		if cmd.Seq > after {
			if cmd.Type == "clipboard_set" && dev.ClipboardSyncDisabled {
				continue
			}
			if cmd.Type == "clipboard_set" && !clipboardCommandMatchesCurrentLocked(dev, cmd) {
				continue
			}
			resp.Commands = append(resp.Commands, cmd)
		}
	}
	if len(dev.Queue) > 500 {
		dev.Queue = append([]inputEvent(nil), dev.Queue[len(dev.Queue)-500:]...)
	}
	return resp
}

func trimAckedInputQueue(queue []inputEvent, after int64) []inputEvent {
	if after <= 0 || len(queue) == 0 {
		return queue
	}
	trim := 0
	for trim < len(queue) && queue[trim].Seq <= after {
		trim++
	}
	if trim == 0 {
		return queue
	}
	if trim >= len(queue) {
		return nil
	}
	return append([]inputEvent(nil), queue[trim:]...)
}

func (h *hub) applyFrameUploadLocked(deviceID, mode, codec string, originX, originY, width, height, quality int, body []byte, tiles []tilePatch) (*deviceState, error) {
	dev := h.ensureDeviceLocked(deviceID)
	dev.SeenAt = time.Now()
	dev.DesktopStatus = "available"
	dev.DesktopMessage = ""
	dev.DesktopAt = time.Now()
	dev.OriginX = originX
	dev.OriginY = originY
	if width > 0 {
		dev.Width = width
	}
	if height > 0 {
		dev.Height = height
	}

	switch mode {
	case "patch":
		if len(tiles) != 1 {
			return nil, errors.New("patch metadata is invalid")
		}
		tile := tiles[0]
		if err := applyPatchLocked(dev, body, codec, tile.X, tile.Y, tile.W, tile.H, quality); err != nil {
			return nil, err
		}
	case "tiles":
		if err := applyTilesLocked(dev, body, codec, tiles, quality); err != nil {
			return nil, err
		}
	default:
		if err := applyFullFrameLocked(dev, body, codec, quality); err != nil {
			return nil, err
		}
	}

	noteUploadLocked(dev, mode, len(body))
	return dev, nil
}

func (h *hub) applyDesktopStatusLocked(deviceID, status, message string) *deviceState {
	dev := h.ensureDeviceLocked(deviceID)
	now := time.Now()
	status = strings.TrimSpace(strings.ToLower(status))
	if status != "unavailable" {
		status = "available"
		message = ""
	}
	message = strings.TrimSpace(message)
	changed := dev.DesktopStatus != status || dev.DesktopMessage != message
	dev.SeenAt = now
	dev.DesktopStatus = status
	dev.DesktopMessage = message
	dev.DesktopAt = now
	if changed {
		notifyLocked(dev)
	}
	return dev
}

func (h *hub) handleFrameUpload(w http.ResponseWriter, r *http.Request) {
	deviceID := strings.TrimSpace(r.URL.Query().Get("deviceId"))
	if deviceID == "" {
		http.Error(w, "deviceId is required", http.StatusBadRequest)
		return
	}

	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	if mode == "" {
		mode = "full"
	}
	codec := normalizeFrameCodec(r.URL.Query().Get("codec"))

	width, _ := strconv.Atoi(r.URL.Query().Get("width"))
	height, _ := strconv.Atoi(r.URL.Query().Get("height"))
	originX := atoiDefault(r.URL.Query().Get("originX"), 0)
	originY := atoiDefault(r.URL.Query().Get("originY"), 0)
	quality := clamp(atoiDefault(r.URL.Query().Get("quality"), 55), 30, 100)

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 12<<20))
	if err != nil {
		http.Error(w, "failed to read frame body", http.StatusBadRequest)
		return
	}
	if len(body) == 0 {
		http.Error(w, "empty frame body", http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	tilesMeta := []tilePatch(nil)
	if mode == "patch" {
		tilesMeta = []tilePatch{{
			X: atoiDefault(r.URL.Query().Get("x"), 0),
			Y: atoiDefault(r.URL.Query().Get("y"), 0),
			W: atoiDefault(r.URL.Query().Get("w"), 0),
			H: atoiDefault(r.URL.Query().Get("h"), 0),
		}}
	} else if mode == "tiles" {
		atlas, tiles, err := readTilesUpload(r.Header.Get("Content-Type"), body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body = atlas
		tilesMeta = tiles
	}
	dev, err := h.applyFrameUploadLocked(deviceID, mode, codec, originX, originY, width, height, quality, body, tilesMeta)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	writeJSON(w, http.StatusOK, h.snapshotLocked(dev))
}

func (h *hub) handleDesktopStatus(w http.ResponseWriter, r *http.Request) {
	var req agentFrameEnvelope
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024)).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	req.DeviceID = strings.TrimSpace(req.DeviceID)
	if req.DeviceID == "" {
		http.Error(w, "deviceId is required", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	dev := h.applyDesktopStatusLocked(req.DeviceID, req.Status, req.Message)
	snapshot := h.snapshotLocked(dev)
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, snapshot)
}

func (h *hub) handleFrameImage(w http.ResponseWriter, r *http.Request) {
	deviceID := strings.TrimSpace(r.URL.Query().Get("deviceId"))
	if deviceID == "" {
		http.Error(w, "deviceId is required", http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	dev, ok := h.devices[deviceID]
	if !ok || len(dev.Frame) == 0 {
		http.Error(w, "frame not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(dev.Frame)
}

func (h *hub) handlePatchImage(w http.ResponseWriter, r *http.Request) {
	deviceID := strings.TrimSpace(r.URL.Query().Get("deviceId"))
	if deviceID == "" {
		http.Error(w, "deviceId is required", http.StatusBadRequest)
		return
	}

	expectedPatchFrameID, _ := strconv.ParseInt(r.URL.Query().Get("patchFrameId"), 10, 64)

	h.mu.Lock()
	defer h.mu.Unlock()

	dev, ok := h.devices[deviceID]
	if !ok || len(dev.Patch) == 0 || !dev.HasPatch {
		http.Error(w, "patch not found", http.StatusNotFound)
		return
	}
	if expectedPatchFrameID > 0 && dev.PatchFrameID != expectedPatchFrameID {
		http.Error(w, "patch expired", http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(dev.Patch)
}

func (h *hub) handleTilesImage(w http.ResponseWriter, r *http.Request) {
	deviceID := strings.TrimSpace(r.URL.Query().Get("deviceId"))
	if deviceID == "" {
		http.Error(w, "deviceId is required", http.StatusBadRequest)
		return
	}

	expectedTilesFrameID, _ := strconv.ParseInt(r.URL.Query().Get("tilesFrameId"), 10, 64)

	h.mu.Lock()
	defer h.mu.Unlock()

	dev, ok := h.devices[deviceID]
	if !ok || len(dev.TilesAtlas) == 0 || !dev.HasTiles {
		http.Error(w, "tiles not found", http.StatusNotFound)
		return
	}
	if expectedTilesFrameID > 0 && dev.TilesFrameID != expectedTilesFrameID {
		http.Error(w, "tiles expired", http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(dev.TilesAtlas)
}

func (h *hub) handleClipboardGet(w http.ResponseWriter, r *http.Request) {
	deviceID := strings.TrimSpace(r.URL.Query().Get("deviceId"))
	if deviceID == "" {
		http.Error(w, "deviceId is required", http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	dev, ok := h.devices[deviceID]
	if !ok {
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}

	snap := clipboardSnapshotLocked(dev)
	after, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("after")), 10, 64)
	if after > 0 && snap.Version <= after {
		snap.Kind = ""
		snap.Text = ""
		snap.Image = nil
		snap.Files = nil
	}
	writeJSON(w, http.StatusOK, snap)
}

func (h *hub) handleClipboardPost(w http.ResponseWriter, r *http.Request) {
	var payload clipboardState
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxClipboardJSONBytes)).Decode(&payload); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	payload.DeviceID = strings.TrimSpace(payload.DeviceID)
	if payload.DeviceID == "" {
		http.Error(w, "deviceId is required", http.StatusBadRequest)
		return
	}
	if clipboardPayloadKind(payload) == "files" {
		h.mu.Lock()
		defer h.mu.Unlock()
		writeJSON(w, http.StatusOK, clipboardSnapshotLocked(h.ensureDeviceLocked(payload.DeviceID)))
		return
	}
	if len(payload.Image) > maxClipboardImageBytes {
		http.Error(w, "clipboard image too large", http.StatusRequestEntityTooLarge)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	writeJSON(w, http.StatusOK, h.applyClipboardLocked(payload))
}

func (h *hub) handleTransferCreate(w http.ResponseWriter, r *http.Request) {
	var req transferCreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	req.DeviceID = strings.TrimSpace(req.DeviceID)
	req.Name = strings.TrimSpace(req.Name)
	req.Direction = strings.TrimSpace(strings.ToLower(req.Direction))
	if req.DeviceID == "" || req.Name == "" || req.Size < 0 {
		http.Error(w, "deviceId, name and size are required", http.StatusBadRequest)
		return
	}
	if req.Direction == "" {
		req.Direction = "controller_to_agent"
	}
	id := randomKey(12)
	tempPath := filepath.Join(h.transferDir, id+".bin")
	if err := os.MkdirAll(h.transferDir, 0o755); err != nil {
		http.Error(w, "failed to create transfer spool", http.StatusInternalServerError)
		return
	}
	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		log.Printf("[srv][transfer] create spool file failed: %v (dir=%s)", err, h.transferDir)
		http.Error(w, "failed to create transfer spool", http.StatusInternalServerError)
		return
	}
	_ = file.Close()
	now := time.Now()
	state := &transferState{
		ID:        id,
		DeviceID:  req.DeviceID,
		Name:      req.Name,
		Direction: req.Direction,
		TempPath:  tempPath,
		Size:      req.Size,
		Status:    "uploading",
		CreatedAt: now,
		UpdatedAt: now,
	}
	h.mu.Lock()
	h.pruneTransfersLocked(now)
	h.transfers[id] = state
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, snapshotTransfer(state))
}

func (h *hub) handleTransferChunkPut(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	offset, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("offset")), 10, 64)
	if err != nil || id == "" || offset < 0 {
		http.Error(w, "invalid id or offset", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		http.Error(w, "failed to read chunk", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	state := h.transfers[id]
	if state == nil {
		h.mu.Unlock()
		http.Error(w, "transfer not found", http.StatusNotFound)
		return
	}
	if state.Status == "canceled" {
		h.mu.Unlock()
		http.Error(w, "transfer canceled", http.StatusConflict)
		return
	}
	tempPath := state.TempPath
	h.mu.Unlock()

	file, err := os.OpenFile(tempPath, os.O_WRONLY, 0o644)
	if err != nil {
		http.Error(w, "failed to open transfer spool", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	if _, err := file.WriteAt(body, offset); err != nil {
		http.Error(w, "failed to write chunk", http.StatusInternalServerError)
		return
	}

	h.mu.Lock()
	state = h.transfers[id]
	if state != nil {
		state.UploadedBytes = maxInt64(state.UploadedBytes, offset+int64(len(body)))
		if !state.UploadDone && state.Status != "receiving" {
			state.Status = "uploading"
		}
		state.UpdatedAt = time.Now()
	}
	snap := snapshotTransfer(state)
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, snap)
}

func (h *hub) handleTransferComplete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	state := h.transfers[id]
	if state == nil {
		h.mu.Unlock()
		http.Error(w, "transfer not found", http.StatusNotFound)
		return
	}
	if state.Status != "canceled" {
		state.UploadDone = true
		state.UploadedBytes = maxInt64(state.UploadedBytes, state.Size)
		if state.DeliveredBytes >= state.Size && state.Size > 0 {
			state.Status = "completed"
		} else if state.DeliveredBytes > 0 || state.Status == "receiving" {
			state.Status = "receiving"
		} else {
			state.Status = "ready"
		}
		state.UpdatedAt = time.Now()
	}
	snap := snapshotTransfer(state)
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, snap)
}

func (h *hub) handleTransferChunkGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	offset, err1 := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("offset")), 10, 64)
	size, err2 := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("size")), 10, 64)
	if id == "" || err1 != nil || err2 != nil || offset < 0 || size <= 0 || size > 8<<20 {
		http.Error(w, "invalid transfer read request", http.StatusBadRequest)
		return
	}
	var tempPath string
	var readSize int64
	for {
		h.mu.Lock()
		state := h.transfers[id]
		if state == nil {
			h.mu.Unlock()
			http.Error(w, "transfer not found", http.StatusNotFound)
			return
		}
		if state.Status == "canceled" {
			h.mu.Unlock()
			http.Error(w, "transfer canceled", http.StatusConflict)
			return
		}
		if state.Status == "failed" {
			errText := nonEmpty(state.Error, "transfer failed")
			h.mu.Unlock()
			http.Error(w, errText, http.StatusGone)
			return
		}
		remaining := state.Size - offset
		if remaining < 0 {
			h.mu.Unlock()
			http.Error(w, "offset beyond end", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if remaining == 0 && state.UploadDone {
			h.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		available := state.UploadedBytes - offset
		if available > 0 {
			readSize = minInt64(size, minInt64(available, remaining))
			tempPath = state.TempPath
			h.mu.Unlock()
			break
		}
		if state.UploadDone {
			h.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.mu.Unlock()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}

	file, err := os.Open(tempPath)
	if err != nil {
		http.Error(w, "failed to open transfer spool", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	buf := make([]byte, int(readSize))
	n, err := file.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		http.Error(w, "failed to read chunk", http.StatusInternalServerError)
		return
	}
	if n == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(n))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf[:n])
}

func (h *hub) handleTransferProgress(w http.ResponseWriter, r *http.Request) {
	var req transferProgressRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024)).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	state := h.transfers[req.ID]
	if state == nil {
		h.mu.Unlock()
		http.Error(w, "transfer not found", http.StatusNotFound)
		return
	}
	state.DeliveredBytes = maxInt64(state.DeliveredBytes, req.DeliveredBytes)
	if req.Status != "" {
		state.Status = req.Status
	}
	if req.Error != "" {
		state.Error = req.Error
	}
	if state.DeliveredBytes >= state.Size && state.Size > 0 && state.Status == "receiving" {
		state.Status = "completed"
	}
	state.UpdatedAt = time.Now()
	snap := snapshotTransfer(state)
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, snap)
}

func (h *hub) handleTransferCancel(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	state := h.transfers[id]
	if state == nil {
		h.mu.Unlock()
		http.Error(w, "transfer not found", http.StatusNotFound)
		return
	}
	state.Status = "canceled"
	if state.Error == "" {
		state.Error = "canceled"
	}
	state.UpdatedAt = time.Now()
	snap := snapshotTransfer(state)
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, snap)
}

func (h *hub) handleTransferList(w http.ResponseWriter, r *http.Request) {
	deviceID := strings.TrimSpace(r.URL.Query().Get("deviceId"))
	now := time.Now()
	h.mu.Lock()
	h.pruneTransfersLocked(now)
	items := make([]transferSnapshot, 0, len(h.transfers))
	for _, state := range h.transfers {
		if deviceID != "" && state.DeviceID != deviceID {
			continue
		}
		items = append(items, snapshotTransfer(state))
	}
	h.mu.Unlock()
	sort.Slice(items, func(i, j int) bool { return items[i].UpdatedAt > items[j].UpdatedAt })
	writeJSON(w, http.StatusOK, map[string]any{"transfers": items})
}

func (h *hub) handleFileResultPost(w http.ResponseWriter, r *http.Request) {
	var result remoteFileResult
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&result); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	result.ID = strings.TrimSpace(result.ID)
	result.DeviceID = strings.TrimSpace(result.DeviceID)
	if result.ID == "" || result.DeviceID == "" {
		http.Error(w, "id and deviceId are required", http.StatusBadRequest)
		return
	}
	result.UpdatedAt = time.Now().Format(time.RFC3339)
	h.mu.Lock()
	h.fileResults[result.ID] = result
	h.pruneFileResultsLocked(time.Now())
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *hub) handleFileResultGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	result, ok := h.fileResults[id]
	h.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *hub) handleCommandResultPost(w http.ResponseWriter, r *http.Request) {
	var result remoteCommandResult
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&result); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	result.ID = strings.TrimSpace(result.ID)
	result.DeviceID = strings.TrimSpace(result.DeviceID)
	if result.ID == "" || result.DeviceID == "" {
		http.Error(w, "id and deviceId are required", http.StatusBadRequest)
		return
	}
	result.UpdatedAt = time.Now().Format(time.RFC3339)
	h.mu.Lock()
	h.commandResults[result.ID] = result
	h.pruneCommandResultsLocked(time.Now())
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *hub) handleCommandResultGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	result, ok := h.commandResults[id]
	h.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *hub) pruneFileResultsLocked(now time.Time) {
	if h == nil {
		return
	}
	for id, result := range h.fileResults {
		updated, err := time.Parse(time.RFC3339, result.UpdatedAt)
		if err != nil || now.Sub(updated) > 30*time.Minute {
			delete(h.fileResults, id)
		}
	}
}

func (h *hub) pruneCommandResultsLocked(now time.Time) {
	if h == nil {
		return
	}
	for id, result := range h.commandResults {
		updated, err := time.Parse(time.RFC3339, result.UpdatedAt)
		if err != nil || now.Sub(updated) > 30*time.Minute {
			delete(h.commandResults, id)
		}
	}
}

func snapshotTransfer(state *transferState) transferSnapshot {
	if state == nil {
		return transferSnapshot{}
	}
	return transferSnapshot{
		ID:             state.ID,
		DeviceID:       state.DeviceID,
		Name:           state.Name,
		Direction:      state.Direction,
		Size:           state.Size,
		UploadedBytes:  state.UploadedBytes,
		DeliveredBytes: state.DeliveredBytes,
		Status:         state.Status,
		Error:          state.Error,
		UpdatedAt:      formatTime(state.UpdatedAt),
	}
}

func (h *hub) pruneTransfersLocked(now time.Time) {
	for id, state := range h.transfers {
		if state == nil {
			delete(h.transfers, id)
			continue
		}
		if now.Sub(state.UpdatedAt) < 2*time.Hour {
			continue
		}
		delete(h.transfers, id)
		if state.TempPath != "" {
			_ = os.Remove(state.TempPath)
		}
	}
}

func (h *hub) handleCursorUpdate(w http.ResponseWriter, r *http.Request) {
	var payload agentPollRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512*1024)).Decode(&payload); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	payload.DeviceID = strings.TrimSpace(payload.DeviceID)
	if payload.DeviceID == "" {
		payload.DeviceID = strings.TrimSpace(r.URL.Query().Get("deviceId"))
	}
	if payload.DeviceID == "" {
		http.Error(w, "deviceId is required", http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	dev := h.ensureDeviceLocked(payload.DeviceID)
	updateCursorLocked(dev, payload)
	dev.SeenAt = time.Now()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *hub) handleState(w http.ResponseWriter, r *http.Request) {
	deviceID := strings.TrimSpace(r.URL.Query().Get("deviceId"))
	if deviceID == "" {
		http.Error(w, "deviceId is required", http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	dev, ok := h.devices[deviceID]
	if !ok {
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}
	markViewerLocked(dev, strings.TrimSpace(r.URL.Query().Get("viewer")))

	writeJSON(w, http.StatusOK, h.snapshotLocked(dev))
}

func (h *hub) handleStateWait(w http.ResponseWriter, r *http.Request) {
	deviceID := strings.TrimSpace(r.URL.Query().Get("deviceId"))
	if deviceID == "" {
		http.Error(w, "deviceId is required", http.StatusBadRequest)
		return
	}

	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	waitMS := clamp(atoiDefault(r.URL.Query().Get("waitMs"), 12000), 1000, 20000)
	deadline := time.Now().Add(time.Duration(waitMS) * time.Millisecond)

	for {
		h.mu.Lock()
		dev, ok := h.devices[deviceID]
		if !ok {
			h.mu.Unlock()
			http.Error(w, "device not found", http.StatusNotFound)
			return
		}
		markViewerLocked(dev, strings.TrimSpace(r.URL.Query().Get("viewer")))

		snapshot := h.snapshotLocked(dev)
		notifyCh := dev.Notify
		shouldReturn := snapshot.FrameID != after || (after == 0 && !snapshot.Online)
		remaining := time.Until(deadline)
		h.mu.Unlock()

		if shouldReturn || remaining <= 0 {
			writeJSON(w, http.StatusOK, snapshot)
			return
		}

		timer := time.NewTimer(remaining)
		select {
		case <-notifyCh:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			writeJSON(w, http.StatusOK, snapshot)
			return
		}
	}
}

func (h *hub) handleInput(w http.ResponseWriter, r *http.Request) {
	var ev inputEvent
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxClipboardJSONBytes)).Decode(&ev); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	ev.DeviceID = strings.TrimSpace(ev.DeviceID)
	ev.Type = strings.TrimSpace(ev.Type)
	if ev.DeviceID == "" || ev.Type == "" {
		http.Error(w, "deviceId and type are required", http.StatusBadRequest)
		return
	}
	if ev.Clipboard != nil && len(ev.Clipboard.Image) > maxClipboardImageBytes {
		http.Error(w, "clipboard image too large", http.StatusRequestEntityTooLarge)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	dev, ok := h.devices[ev.DeviceID]
	if !ok {
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}
	if ev.Type == "clipboard_toggle" {
		enabled := true
		if ev.ClipboardSync != nil {
			enabled = *ev.ClipboardSync
		}
		if dev.ClipboardSyncDisabled == enabled {
			dev.ClipboardSyncDisabled = !enabled
			notifyLocked(dev)
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok":            true,
			"seq":           dev.NextSeq,
			"clipboardSync": enabled,
		})
		return
	}
	if ev.Type == "clipboard_set" && dev.ClipboardSyncDisabled {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok":            true,
			"ignored":       true,
			"seq":           dev.NextSeq,
			"clipboardSync": false,
		})
		return
	}
	if ev.Type == "clipboard_set" && inputClipboardKind(ev) == "files" {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok":      true,
			"ignored": true,
			"seq":     dev.NextSeq,
		})
		return
	}
	ev, _ = enqueueInputLocked(dev, ev)

	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":  true,
		"seq": ev.Seq,
	})
}

func (h *hub) handleCommands(w http.ResponseWriter, r *http.Request) {
	deviceID := strings.TrimSpace(r.URL.Query().Get("deviceId"))
	if deviceID == "" {
		http.Error(w, "deviceId is required", http.StatusBadRequest)
		return
	}

	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)

	h.mu.Lock()
	defer h.mu.Unlock()

	dev, ok := h.devices[deviceID]
	if !ok {
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, h.buildCommandResponseLocked(dev, after, &agentPollRequest{
		After:         after,
		CursorX:       atoiDefault(r.URL.Query().Get("cursorX"), dev.CursorX),
		CursorY:       atoiDefault(r.URL.Query().Get("cursorY"), dev.CursorY),
		CursorVisible: atoiDefault(r.URL.Query().Get("cursorVisible"), boolToInt(dev.CursorVisible)) != 0,
		CursorKind:    r.URL.Query().Get("cursorKind"),
	}))
}

func (h *hub) handleControllerWS(w http.ResponseWriter, r *http.Request) {
	deviceID := strings.TrimSpace(r.URL.Query().Get("deviceId"))
	if deviceID == "" {
		http.Error(w, "deviceId is required", http.StatusBadRequest)
		return
	}

	conn, err := controllerUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	configureServerTCP(conn.UnderlyingConn())
	conn.EnableWriteCompression(false)

	sub := &frameSubscriber{
		Notify:         make(chan struct{}, 1),
		Done:           make(chan struct{}),
		Transport:      "ws",
		PreferredCodec: "jpeg",
		LastPingAt:     time.Now(),
	}

	h.mu.Lock()
	dev, ok := h.devices[deviceID]
	if !ok {
		h.mu.Unlock()
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "device not found"), time.Now().Add(2*time.Second))
		_ = conn.Close()
		return
	}
	if dev.Subscribers == nil {
		dev.Subscribers = make(map[*frameSubscriber]struct{})
	}
	dev.Subscribers[sub] = struct{}{}
	markViewerLocked(dev, "controller")
	notifySubscriber(sub)
	h.mu.Unlock()

	defer func() {
		close(sub.Done)
		h.mu.Lock()
		if dev, ok := h.devices[deviceID]; ok && dev.Subscribers != nil {
			delete(dev.Subscribers, sub)
		}
		h.mu.Unlock()
		_ = conn.Close()
	}()

	conn.SetReadLimit(8 << 10)
	if err := conn.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- h.writeControllerFrames(conn, deviceID, sub)
	}()

	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			select {
			case errCh <- err:
			default:
			}
			break
		}
		if err := conn.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
			break
		}
		if messageType != websocket.TextMessage {
			continue
		}

		var msg wsClientMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		h.mu.Lock()
		dev, ok := h.devices[deviceID]
		if ok {
			markViewerLocked(dev, "controller")
		}
		if ok {
			if !h.handleControllerControlMessageLocked(dev, sub, msg, deviceID) {
				h.mu.Unlock()
				continue
			}
		}
		h.mu.Unlock()
	}

	select {
	case <-errCh:
	default:
	}
}

func (h *hub) serveQUIC(addr string) error {
	tlsConfig, err := generateQUICServerTLSConfig()
	if err != nil {
		return err
	}

	listener, err := quic.ListenAddr(addr, tlsConfig, &quic.Config{
		EnableDatagrams: true,
		KeepAlivePeriod: 5 * time.Second,
		MaxIdleTimeout:  30 * time.Second,
	})
	if err != nil {
		return err
	}

	log.Printf("quic server listening on udp://%s", displayListenHost(addr))
	for {
		conn, err := listener.Accept(context.Background())
		if err != nil {
			return err
		}
		go h.handleQUIC(conn)
	}
}

func (h *hub) handleQUIC(conn *quic.Conn) {
	defer conn.CloseWithError(0, "")

	stream, err := conn.AcceptStream(context.Background())
	if err != nil {
		return
	}
	defer stream.Close()

	if err := stream.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return
	}

	decoder := json.NewDecoder(stream)
	var hello wsClientMessage
	if err := decoder.Decode(&hello); err != nil {
		_ = conn.CloseWithError(1, "invalid hello")
		return
	}

	role := strings.TrimSpace(strings.ToLower(hello.Role))
	if role == "agent" {
		h.handleAgentQUIC(conn, stream, decoder, hello)
		return
	}
	h.handleControllerQUIC(conn, stream, decoder, hello)
}

func (h *hub) handleControllerQUIC(conn *quic.Conn, stream *quic.Stream, decoder *json.Decoder, hello wsClientMessage) {
	deviceID := strings.TrimSpace(hello.DeviceID)
	if strings.TrimSpace(strings.ToLower(hello.Type)) != "hello" || deviceID == "" {
		_ = conn.CloseWithError(1, "deviceId is required")
		return
	}

	sub := &frameSubscriber{
		Notify:         make(chan struct{}, 1),
		Done:           make(chan struct{}),
		Transport:      "quic",
		PreferredCodec: normalizeFrameCodec(hello.Codec),
		LastPingAt:     time.Now(),
	}
	applyControllerPreferencesLocked(sub, hello)

	h.mu.Lock()
	dev, ok := h.devices[deviceID]
	if !ok {
		h.mu.Unlock()
		_ = conn.CloseWithError(2, "device not found")
		return
	}
	if dev.Subscribers == nil {
		dev.Subscribers = make(map[*frameSubscriber]struct{})
	}
	dev.Subscribers[sub] = struct{}{}
	markViewerLocked(dev, "controller")
	notifySubscriber(sub)
	h.mu.Unlock()

	defer func() {
		close(sub.Done)
		h.mu.Lock()
		if dev, ok := h.devices[deviceID]; ok && dev.Subscribers != nil {
			delete(dev.Subscribers, sub)
		}
		h.mu.Unlock()
	}()

	if err := stream.SetReadDeadline(time.Time{}); err != nil {
		return
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- h.writeQUICFrames(conn, deviceID, sub)
	}()

	for {
		var msg wsClientMessage
		if err := decoder.Decode(&msg); err != nil {
			select {
			case errCh <- err:
			default:
			}
			break
		}

		h.mu.Lock()
		dev, ok := h.devices[deviceID]
		if ok {
			markViewerLocked(dev, "controller")
			h.handleControllerControlMessageLocked(dev, sub, msg, deviceID)
		}
		h.mu.Unlock()
	}

	select {
	case <-errCh:
	default:
	}
}

func (h *hub) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	conn, err := controllerUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	configureServerTCP(conn.UnderlyingConn())
	conn.EnableWriteCompression(false)
	defer conn.Close()

	conn.SetReadLimit(8 << 20)
	if err := conn.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return
	}

	messageType, data, err := conn.ReadMessage()
	if err != nil {
		return
	}
	if messageType != websocket.TextMessage {
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "hello is required"), time.Now().Add(2*time.Second))
		return
	}

	var hello wsClientMessage
	if err := json.Unmarshal(data, &hello); err != nil {
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "invalid hello"), time.Now().Add(2*time.Second))
		return
	}
	deviceID := strings.TrimSpace(hello.DeviceID)
	if strings.TrimSpace(strings.ToLower(hello.Type)) != "hello" || strings.TrimSpace(strings.ToLower(hello.Role)) != "agent" || deviceID == "" {
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "deviceId is required"), time.Now().Add(2*time.Second))
		return
	}
	h.mu.Lock()
	h.setAgentSessionLocked(deviceID, true)
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.setAgentSessionLocked(deviceID, false)
		h.mu.Unlock()
	}()
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return
	}

	for {
		messageType, data, err = conn.ReadMessage()
		if err != nil {
			return
		}
		if messageType != websocket.BinaryMessage {
			continue
		}

		header, payload, err := decodeAgentFramePacket(data)
		if err != nil {
			log.Printf("[srv][agent/ws] frame decode failed device=%s: %v", deviceID, err)
			continue
		}
		if header.DeviceID == "" {
			header.DeviceID = deviceID
		}
		if header.DeviceID != deviceID {
			log.Printf("[srv][agent/ws] frame device mismatch stream=%s packet=%s", deviceID, header.DeviceID)
			continue
		}
		if strings.TrimSpace(strings.ToLower(header.Type)) == "desktop_status" {
			h.mu.Lock()
			h.applyDesktopStatusLocked(deviceID, header.Status, header.Message)
			h.mu.Unlock()
			continue
		}
		if strings.TrimSpace(strings.ToLower(header.Type)) != "frame" {
			log.Printf("[srv][agent/ws] unsupported packet type device=%s type=%s", deviceID, header.Type)
			continue
		}

		h.mu.Lock()
		_, applyErr := h.applyFrameUploadLocked(
			deviceID,
			header.Mode,
			header.Codec,
			header.OriginX,
			header.OriginY,
			header.Width,
			header.Height,
			header.Quality,
			payload,
			header.Tiles,
		)
		h.mu.Unlock()
		if applyErr != nil {
			log.Printf("[srv][agent/ws] frame apply failed device=%s mode=%s: %v", deviceID, header.Mode, applyErr)
		}
	}
}

func (h *hub) handleAgentQUIC(conn *quic.Conn, stream *quic.Stream, decoder *json.Decoder, hello wsClientMessage) {
	deviceID := strings.TrimSpace(hello.DeviceID)
	if strings.TrimSpace(strings.ToLower(hello.Type)) != "hello" || deviceID == "" {
		_ = conn.CloseWithError(1, "deviceId is required")
		return
	}
	h.mu.Lock()
	h.setAgentSessionLocked(deviceID, true)
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.setAgentSessionLocked(deviceID, false)
		h.mu.Unlock()
	}()

	if state := conn.ConnectionState(); !state.SupportsDatagrams.Remote {
		_ = conn.CloseWithError(2, "quic datagrams are required")
		return
	}
	if err := stream.SetReadDeadline(time.Time{}); err != nil {
		return
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- h.readAgentQUICDatagrams(conn, deviceID)
	}()

	encoder := json.NewEncoder(stream)
	for {
		var msg agentStreamMessage
		if err := decoder.Decode(&msg); err != nil {
			select {
			case errCh <- err:
			default:
			}
			break
		}

		switch strings.TrimSpace(strings.ToLower(msg.Type)) {
		case "register":
			if msg.Register == nil {
				if err := encoder.Encode(agentStreamMessage{Type: "error", DeviceID: deviceID, Error: "register payload is required"}); err != nil {
					break
				}
				continue
			}
			req := *msg.Register
			req.DeviceID = deviceID
			h.mu.Lock()
			dev, wasOnline := h.applyRegisterLocked(req)
			h.mu.Unlock()
			if !wasOnline {
				log.Printf("[srv][agent/quic] online device=%s name=%s desktop=%dx%d origin=(%d,%d)", dev.DeviceID, dev.Name, dev.Width, dev.Height, dev.OriginX, dev.OriginY)
			}
		case "clipboard":
			if msg.Clipboard == nil {
				if err := encoder.Encode(agentStreamMessage{Type: "error", DeviceID: deviceID, Error: "clipboard payload is required"}); err != nil {
					break
				}
				continue
			}
			payload := *msg.Clipboard
			payload.DeviceID = deviceID
			h.mu.Lock()
			h.applyClipboardLocked(payload)
			h.mu.Unlock()
		case "poll":
			if msg.Poll == nil {
				if err := encoder.Encode(agentStreamMessage{Type: "error", DeviceID: deviceID, Error: "poll payload is required"}); err != nil {
					break
				}
				continue
			}
			poll := *msg.Poll
			h.mu.Lock()
			dev := h.ensureDeviceLocked(deviceID)
			resp := h.buildCommandResponseLocked(dev, poll.After, &poll)
			h.mu.Unlock()
			if err := encoder.Encode(agentStreamMessage{
				Type:     "commands",
				DeviceID: deviceID,
				Commands: &resp,
			}); err != nil {
				select {
				case errCh <- err:
				default:
				}
				break
			}
		default:
			if err := encoder.Encode(agentStreamMessage{Type: "error", DeviceID: deviceID, Error: "unsupported agent message type"}); err != nil {
				select {
				case errCh <- err:
				default:
				}
				break
			}
		}
	}

	select {
	case <-errCh:
	default:
	}
}

func (h *hub) readAgentQUICDatagrams(conn *quic.Conn, deviceID string) error {
	assembler := quicDatagramAssembler{}
	for {
		data, err := conn.ReceiveDatagram(context.Background())
		if err != nil {
			return err
		}
		packet, ready, err := assembler.Push(data, quicDatagramMagicUplink)
		if err != nil {
			log.Printf("[srv][agent/quic] datagram drop device=%s: %v", deviceID, err)
			continue
		}
		if !ready {
			continue
		}

		header, payload, err := decodeAgentFramePacket(packet)
		if err != nil {
			log.Printf("[srv][agent/quic] frame decode failed device=%s: %v", deviceID, err)
			continue
		}
		if header.DeviceID == "" {
			header.DeviceID = deviceID
		}
		if header.DeviceID != deviceID {
			log.Printf("[srv][agent/quic] frame device mismatch stream=%s packet=%s", deviceID, header.DeviceID)
			continue
		}
		if strings.TrimSpace(strings.ToLower(header.Type)) == "desktop_status" {
			h.mu.Lock()
			h.applyDesktopStatusLocked(deviceID, header.Status, header.Message)
			h.mu.Unlock()
			continue
		}
		if strings.TrimSpace(strings.ToLower(header.Type)) != "frame" {
			log.Printf("[srv][agent/quic] unsupported packet type device=%s type=%s", deviceID, header.Type)
			continue
		}

		h.mu.Lock()
		_, applyErr := h.applyFrameUploadLocked(
			deviceID,
			header.Mode,
			header.Codec,
			header.OriginX,
			header.OriginY,
			header.Width,
			header.Height,
			header.Quality,
			payload,
			header.Tiles,
		)
		h.mu.Unlock()
		if applyErr != nil {
			log.Printf("[srv][agent/quic] frame apply failed device=%s mode=%s: %v", deviceID, header.Mode, applyErr)
		}
	}
}

func (h *hub) ensureDeviceLocked(deviceID string) *deviceState {
	if dev, ok := h.devices[deviceID]; ok {
		return dev
	}

	dev := &deviceState{
		DeviceID:    deviceID,
		Notify:      make(chan struct{}, 1),
		Subscribers: make(map[*frameSubscriber]struct{}),
	}
	h.devices[deviceID] = dev
	return dev
}

func (h *hub) snapshotLocked(dev *deviceState) deviceState {
	now := time.Now()
	return deviceState{
		DeviceID:         dev.DeviceID,
		Name:             dev.Name,
		Alias:            dev.Alias,
		OriginX:          dev.OriginX,
		OriginY:          dev.OriginY,
		Width:            dev.Width,
		Height:           dev.Height,
		FrameID:          dev.FrameID,
		UpdateKind:       dev.UpdateKind,
		DesktopStatus:    dev.DesktopStatus,
		DesktopMessage:   dev.DesktopMessage,
		DesktopUpdated:   formatTime(dev.DesktopAt),
		HasPatch:         dev.HasPatch,
		PatchFrameID:     dev.PatchFrameID,
		PatchBaseFrameID: dev.PatchBaseFrameID,
		PatchX:           dev.PatchX,
		PatchY:           dev.PatchY,
		PatchW:           dev.PatchW,
		PatchH:           dev.PatchH,
		HasTiles:         dev.HasTiles,
		TilesFrameID:     dev.TilesFrameID,
		TilesBaseFrameID: dev.TilesBaseFrameID,
		Tiles:            cloneTiles(dev.Tiles),
		Online:           deviceOnlineLocked(dev, now),
		LastSeen:         formatTime(dev.SeenAt),
		UpdatedAt:        formatTime(dev.FrameAt),
		HasFrame:         len(dev.Frame) > 0,
		Clipboard:        dev.ClipboardText,
		ClipboardVersion: dev.ClipboardVersion,
		ClipboardUpdated: formatTime(dev.ClipboardAt),
		Commands:         len(dev.Queue),
		CursorX:          dev.CursorX,
		CursorY:          dev.CursorY,
		CursorVisible:    dev.CursorVisible,
		CursorKind:       dev.CursorKind,
	}
}

func updateCursorLocked(dev *deviceState, cursor agentPollRequest) {
	if dev == nil {
		return
	}
	x := cursor.CursorX
	y := cursor.CursorY
	visible := cursor.CursorVisible
	kind := normalizeCursorKind(cursor.CursorKind)
	shapeChanged := dev.CursorVisible != visible || dev.CursorKind != kind
	imageChanged := len(dev.CursorImage) > 0 || dev.CursorImageW != 0 || dev.CursorImageH != 0 || dev.CursorHotX != 0 || dev.CursorHotY != 0
	if imageChanged {
		dev.CursorImage = nil
		dev.CursorImageW = 0
		dev.CursorImageH = 0
		dev.CursorHotX = 0
		dev.CursorHotY = 0
	}
	if shapeChanged || imageChanged {
		dev.CursorVersion++
	}
	if dev.CursorX == x && dev.CursorY == y && dev.CursorVisible == visible && dev.CursorKind == kind && !imageChanged {
		return
	}
	dev.CursorX = x
	dev.CursorY = y
	dev.CursorVisible = visible
	dev.CursorKind = kind
	notifyLocked(dev)
}

func ensureFrameImageLocked(dev *deviceState) error {
	if dev == nil {
		return errors.New("device is missing")
	}
	if dev.Image != nil {
		return nil
	}
	if len(dev.Frame) == 0 {
		return errors.New("base frame is missing, client should send a full frame")
	}
	if normalizeFrameCodec(dev.FrameCodec) != "jpeg" {
		return errors.New("base frame codec is not patch-compatible")
	}
	img, err := decodeJPEGToRGBA(dev.Frame)
	if err != nil {
		return errors.New("failed to decode base frame")
	}
	dev.Image = img
	return nil
}

func applyFullFrameLocked(dev *deviceState, body []byte, codec string, quality int) error {
	dev.FrameID++
	dev.Frame = bytes.Clone(body)
	dev.FrameCodec = normalizeFrameCodec(codec)
	dev.FrameQuality = quality
	dev.FullFrameID = dev.FrameID
	dev.Image = nil
	dev.FrameAt = time.Now()
	dev.UpdateKind = "full"
	dev.HasPatch = false
	dev.Patch = nil
	dev.PatchFrameID = 0
	dev.PatchBaseFrameID = 0
	dev.PatchX = 0
	dev.PatchY = 0
	dev.PatchW = 0
	dev.PatchH = 0
	dev.HasTiles = false
	dev.TilesAtlas = nil
	dev.TilesFrameID = 0
	dev.TilesBaseFrameID = 0
	dev.Tiles = nil
	rememberFrameUpdateLocked(dev, recentFrameUpdate{
		FrameID:    dev.FrameID,
		UpdateKind: "full",
		Codec:      dev.FrameCodec,
		Quality:    quality,
		Payload:    dev.Frame,
	})
	notifyLocked(dev)
	return nil
}

func applyPatchLocked(dev *deviceState, body []byte, codec string, x, y, patchW, patchH, quality int) error {
	if len(dev.Frame) == 0 {
		return errors.New("base frame is missing, client should send a full frame")
	}
	if err := ensureFrameImageLocked(dev); err != nil {
		return err
	}
	if patchW <= 0 || patchH <= 0 {
		return errors.New("invalid patch size")
	}
	if x < 0 || y < 0 || x+patchW > dev.Image.Bounds().Dx() || y+patchH > dev.Image.Bounds().Dy() {
		return errors.New("patch is out of bounds")
	}

	codec = normalizeFrameCodec(codec)
	patchImg, err := decodeIncrementalToRGBA(codec, body, patchW, patchH)
	if err != nil {
		return fmt.Errorf("invalid patch %s body", codec)
	}
	if patchImg.Bounds().Dx() != patchW || patchImg.Bounds().Dy() != patchH {
		return errors.New("patch size does not match image body")
	}

	baseFrameID := dev.FrameID
	draw.Draw(dev.Image, image.Rect(x, y, x+patchW, y+patchH), patchImg, patchImg.Bounds().Min, draw.Src)

	dev.FrameID++
	dev.FrameCodec = "jpeg"
	dev.FrameQuality = quality
	dev.Patch = bytes.Clone(body)
	dev.FrameAt = time.Now()
	dev.UpdateKind = "patch"
	dev.HasPatch = true
	dev.PatchFrameID = dev.FrameID
	dev.PatchBaseFrameID = baseFrameID
	dev.PatchX = x
	dev.PatchY = y
	dev.PatchW = patchW
	dev.PatchH = patchH
	dev.HasTiles = false
	dev.TilesAtlas = nil
	dev.TilesFrameID = 0
	dev.TilesBaseFrameID = 0
	dev.Tiles = nil
	rememberFrameUpdateLocked(dev, recentFrameUpdate{
		FrameID:     dev.FrameID,
		UpdateKind:  "patch",
		BaseFrameID: baseFrameID,
		Codec:       codec,
		Quality:     quality,
		Payload:     dev.Patch,
		PatchX:      x,
		PatchY:      y,
		PatchW:      patchW,
		PatchH:      patchH,
	})
	notifyLocked(dev)
	return nil
}

func applyTilesLocked(dev *deviceState, atlas []byte, codec string, tiles []tilePatch, quality int) error {
	if len(dev.Frame) == 0 {
		return errors.New("base frame is missing, client should send a full frame")
	}
	if err := ensureFrameImageLocked(dev); err != nil {
		return err
	}
	if len(tiles) == 0 {
		return errors.New("tile list is empty")
	}

	atlasW, atlasH := tileAtlasSize(tiles)
	if atlasW <= 0 || atlasH <= 0 {
		return errors.New("tile atlas metadata is invalid")
	}
	codec = normalizeFrameCodec(codec)
	atlasImg, err := decodeIncrementalToRGBA(codec, atlas, atlasW, atlasH)
	if err != nil {
		return fmt.Errorf("invalid tiles %s body", codec)
	}

	baseFrameID := dev.FrameID
	for _, tile := range tiles {
		if tile.W <= 0 || tile.H <= 0 {
			return errors.New("invalid tile size")
		}
		if tile.X < 0 || tile.Y < 0 || tile.X+tile.W > dev.Image.Bounds().Dx() || tile.Y+tile.H > dev.Image.Bounds().Dy() {
			return errors.New("tile is out of bounds")
		}
		if tile.AtlasX < 0 || tile.AtlasY < 0 || tile.AtlasX+tile.W > atlasImg.Bounds().Dx() || tile.AtlasY+tile.H > atlasImg.Bounds().Dy() {
			return errors.New("tile atlas rect is out of bounds")
		}

		draw.Draw(
			dev.Image,
			image.Rect(tile.X, tile.Y, tile.X+tile.W, tile.Y+tile.H),
			atlasImg,
			image.Point{X: tile.AtlasX, Y: tile.AtlasY},
			draw.Src,
		)
	}

	dev.FrameID++
	dev.FrameCodec = "jpeg"
	dev.FrameQuality = quality
	dev.Patch = nil
	dev.TilesAtlas = bytes.Clone(atlas)
	dev.FrameAt = time.Now()
	dev.UpdateKind = "tiles"
	dev.HasPatch = false
	dev.PatchFrameID = 0
	dev.PatchBaseFrameID = 0
	dev.PatchX = 0
	dev.PatchY = 0
	dev.PatchW = 0
	dev.PatchH = 0
	dev.HasTiles = true
	dev.TilesFrameID = dev.FrameID
	dev.TilesBaseFrameID = baseFrameID
	dev.Tiles = cloneTiles(tiles)
	rememberFrameUpdateLocked(dev, recentFrameUpdate{
		FrameID:     dev.FrameID,
		UpdateKind:  "tiles",
		BaseFrameID: baseFrameID,
		Codec:       codec,
		Quality:     quality,
		Payload:     dev.TilesAtlas,
		Tiles:       cloneTiles(dev.Tiles),
	})
	notifyLocked(dev)
	return nil
}

func decodeJPEGToRGBA(data []byte) (*image.RGBA, error) {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	bounds := img.Bounds()
	rgba := image.NewRGBA(bounds)
	draw.Draw(rgba, bounds, img, bounds.Min, draw.Src)
	return rgba, nil
}

func decodeIncrementalToRGBA(codec string, data []byte, width, height int) (*image.RGBA, error) {
	switch normalizeFrameCodec(codec) {
	case "bgra":
		return decodeBGRAToRGBA(data, width, height)
	default:
		return decodeJPEGToRGBA(data)
	}
}

func decodeBGRAToRGBA(data []byte, width, height int) (*image.RGBA, error) {
	if width <= 0 || height <= 0 {
		return nil, errors.New("invalid bgra dimensions")
	}
	expected := width * height * 4
	if len(data) != expected {
		return nil, fmt.Errorf("bgra payload size mismatch: got %d want %d", len(data), expected)
	}
	rgba := image.NewRGBA(image.Rect(0, 0, width, height))
	for srcOffset, dstOffset := 0, 0; srcOffset+3 < len(data) && dstOffset+3 < len(rgba.Pix); srcOffset, dstOffset = srcOffset+4, dstOffset+4 {
		rgba.Pix[dstOffset] = data[srcOffset+2]
		rgba.Pix[dstOffset+1] = data[srcOffset+1]
		rgba.Pix[dstOffset+2] = data[srcOffset]
		rgba.Pix[dstOffset+3] = 255
	}
	return rgba, nil
}

func tileAtlasSize(tiles []tilePatch) (int, int) {
	maxX, maxY := 0, 0
	for _, tile := range tiles {
		if tile.W > 0 && tile.AtlasX+tile.W > maxX {
			maxX = tile.AtlasX + tile.W
		}
		if tile.H > 0 && tile.AtlasY+tile.H > maxY {
			maxY = tile.AtlasY + tile.H
		}
	}
	return maxX, maxY
}

func rememberFrameUpdateLocked(dev *deviceState, update recentFrameUpdate) {
	if dev == nil || update.FrameID <= 0 || len(update.Payload) == 0 {
		return
	}
	update.Codec = normalizeFrameCodec(update.Codec)
	update.Payload = bytes.Clone(update.Payload)
	update.Tiles = cloneTiles(update.Tiles)
	dev.RecentUpdates = append(dev.RecentUpdates, update)
	if len(dev.RecentUpdates) > 24 {
		dev.RecentUpdates = append([]recentFrameUpdate(nil), dev.RecentUpdates[len(dev.RecentUpdates)-24:]...)
	}
}

func selectRecentIncrementalLocked(dev *deviceState, ackFrameID int64) (recentFrameUpdate, bool) {
	if dev == nil {
		return recentFrameUpdate{}, false
	}
	for _, update := range dev.RecentUpdates {
		if update.FrameID <= ackFrameID || len(update.Payload) == 0 {
			continue
		}
		switch update.UpdateKind {
		case "patch", "tiles":
			if update.BaseFrameID == ackFrameID {
				return update, true
			}
		}
	}
	return recentFrameUpdate{}, false
}

func selectSubscriberIncrementalLocked(dev *deviceState, sub *frameSubscriber) (recentFrameUpdate, bool) {
	if sub == nil {
		return recentFrameUpdate{}, false
	}
	if sub.LastSentFrameID > sub.AckFrameID {
		if !canPipelineIncrementalLocked(sub) {
			return recentFrameUpdate{}, false
		}
		return selectRecentIncrementalLocked(dev, sub.LastSentFrameID)
	}
	return selectRecentIncrementalLocked(dev, sub.AckFrameID)
}

func canPipelineIncrementalLocked(sub *frameSubscriber) bool {
	if sub == nil || sub.SlowScore > 0 {
		return false
	}
	outstanding := sub.LastSentFrameID - sub.AckFrameID
	if outstanding <= 0 {
		return true
	}

	limit := int64(2)
	targetKBps := maxInt(sub.CurrentTargetKBps, activeServerTuning.TargetKBps)
	if targetKBps <= 0 {
		targetKBps = activeServerTuning.AutoTargetMinKBps
	}
	if targetKBps >= 768 && sub.AvgFrameBytes > 0 && sub.AvgFrameBytes <= 64*1024 {
		limit = 3
	}
	if strings.EqualFold(strings.TrimSpace(sub.Transport), "quic") {
		limit = 4
	}
	if targetKBps > 0 && sub.RecentBPS > float64(targetKBps)*1024*0.90 {
		limit = 1
	}
	if sub.LastAckAt.IsZero() {
		return false
	}
	if time.Since(sub.LastAckAt) > 1200*time.Millisecond {
		return false
	}
	return outstanding < limit
}

func shouldContinueFramePumpLocked(dev *deviceState, sub *frameSubscriber) bool {
	if dev == nil || sub == nil || sub.Paused || dev.FrameID <= sub.LastSentFrameID {
		return false
	}
	_, ok := selectSubscriberIncrementalLocked(dev, sub)
	return ok
}

func shouldHoldFullFallback(sub *frameSubscriber) bool {
	if sub == nil || sub.LastSentFrameID <= sub.AckFrameID || sub.LastSentAt.IsZero() {
		return false
	}
	wait := 1500 * time.Millisecond
	if strings.EqualFold(strings.TrimSpace(sub.Transport), "quic") {
		wait = 350 * time.Millisecond
	}
	return time.Since(sub.LastSentAt) < wait
}

func encodeJPEG(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: clamp(quality, 30, 100)}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func atoiDefault(raw string, fallback int) int {
	if value, err := strconv.Atoi(raw); err == nil {
		return value
	}
	return fallback
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}

func formatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	value := float64(n)
	units := []string{"KB", "MB", "GB", "TB"}
	for _, unit := range units {
		value /= 1024
		if value < 1024 || unit == units[len(units)-1] {
			return fmt.Sprintf("%.2f%s", value, unit)
		}
	}
	return fmt.Sprintf("%dB", n)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if activeRequestLogging {
			log.Printf("[srv][http] %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
		}
		next.ServeHTTP(w, r)
	})
}

func recoverHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("panic recovered in %s %s: %v", r.Method, r.URL.Path, recovered)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func clamp(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func nonEmpty(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func normalizeCursorKind(kind string) string {
	switch strings.TrimSpace(strings.ToLower(kind)) {
	case "text", "wait", "hand", "cross", "size-we", "size-ns", "size-nwse", "size-nesw", "size-all", "forbidden", "help":
		return strings.TrimSpace(strings.ToLower(kind))
	default:
		return "arrow"
	}
}

func readTilesUpload(contentType string, body []byte) ([]byte, []tilePatch, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		return nil, nil, errors.New("tiles upload requires multipart/form-data")
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil, nil, errors.New("tiles upload boundary is missing")
	}

	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	var atlasData []byte
	var tilesJSON []byte

	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, errors.New("failed to read multipart tiles body")
		}

		data, err := io.ReadAll(io.LimitReader(part, 12<<20))
		if err != nil {
			part.Close()
			return nil, nil, errors.New("failed to read tiles part")
		}
		name := part.FormName()
		part.Close()

		switch name {
		case "atlas":
			atlasData = append([]byte(nil), data...)
		case "tiles":
			tilesJSON = append([]byte(nil), data...)
		}
	}

	if len(atlasData) == 0 {
		return nil, nil, errors.New("tiles atlas is missing")
	}
	if len(tilesJSON) == 0 {
		return nil, nil, errors.New("tiles metadata is missing")
	}

	var tiles []tilePatch
	if err := json.Unmarshal(tilesJSON, &tiles); err != nil {
		return nil, nil, errors.New("invalid tiles metadata")
	}
	return atlasData, tiles, nil
}

func cloneTiles(src []tilePatch) []tilePatch {
	if len(src) == 0 {
		return nil
	}
	dst := make([]tilePatch, len(src))
	copy(dst, src)
	return dst
}

func markViewerLocked(dev *deviceState, viewer string) {
	viewer = strings.TrimSpace(strings.ToLower(viewer))
	if viewer == "" {
		return
	}
	dev.ViewerKind = viewer
	dev.ViewerAt = time.Now()
}

func viewerProfileLocked(dev *deviceState) (string, string, string, int, int, int, bool) {
	if dev == nil {
		return "", "", "", 0, 0, 0, false
	}

	now := time.Now()
	if len(dev.Subscribers) > 0 {
		fps := activeServerTuning.ControllerQUICFPS
		quality := activeServerTuning.ControllerQUICQuality
		fullMS := activeServerTuning.ControllerQUICFullMS
		mode := activeServerTuning.ControllerQUICMode
		codec := "jpeg"
		first := true
		h264Seen := false
		activeCount := 0
		for sub := range dev.Subscribers {
			if sub == nil || sub.Paused {
				continue
			}
			activeCount++
			subMode, subCodec, subFPS, subQuality, subFullMS := subscriberProfileLocked(sub, now)
			if normalizeFrameCodec(sub.PreferredCodec) == "h264" && strings.EqualFold(strings.TrimSpace(sub.Transport), "quic") {
				h264Seen = true
			}
			if first || subFPS < fps {
				fps = subFPS
			}
			if first || subQuality < quality {
				quality = subQuality
			}
			if first || subFullMS > fullMS {
				fullMS = subFullMS
			}
			if !first && subMode == "delta" {
				mode = "delta"
			} else if first {
				mode = subMode
			}
			if first {
				codec = subCodec
			} else if !h264Seen && codec != subCodec {
				codec = "jpeg"
			}
			first = false
		}
		if activeCount == 0 {
			return "", "", "", 0, 0, 0, false
		}
		if h264Seen {
			mode = "video"
			codec = "h264"
		}
		if normalizeStreamMode(mode, "video") != "video" {
			codec = "jpeg"
		}
		return "controller", normalizeStreamMode(mode, "video"), normalizeFrameCodec(codec), clamp(fps, 8, activeServerTuning.MaxAutoFPS), clamp(quality, 40, 100), clamp(fullMS, 1800, 15000), true
	}

	if now.Sub(dev.ViewerAt) > 5*time.Second {
		return "", "", "", 0, 0, 0, false
	}
	switch dev.ViewerKind {
	case "controller":
		return "controller", activeServerTuning.ControllerWSMode, "jpeg", activeServerTuning.ControllerWSFPS, activeServerTuning.ControllerWSQuality, activeServerTuning.ControllerWSFullMS, true
	case "web":
		return "web", activeServerTuning.WebMode, "jpeg", activeServerTuning.WebFPS, activeServerTuning.WebQuality, activeServerTuning.WebFullMS, true
	default:
		return dev.ViewerKind, activeServerTuning.WebMode, "jpeg", activeServerTuning.WebFPS, activeServerTuning.WebQuality, activeServerTuning.WebFullMS, true
	}
}

func subscriberProfileLocked(sub *frameSubscriber, now time.Time) (string, string, int, int, int) {
	if sub == nil {
		return activeServerTuning.ControllerWSMode, "jpeg", activeServerTuning.ControllerWSFPS, activeServerTuning.ControllerWSQuality, activeServerTuning.ControllerWSFullMS
	}

	transport := strings.TrimSpace(strings.ToLower(sub.Transport))
	fps := activeServerTuning.ControllerWSFPS
	quality := activeServerTuning.ControllerWSQuality
	fullMS := activeServerTuning.ControllerWSFullMS
	mode := "video"
	if transport == "quic" {
		fps = activeServerTuning.ControllerQUICFPS
		quality = activeServerTuning.ControllerQUICQuality
		fullMS = activeServerTuning.ControllerQUICFullMS
		mode = activeServerTuning.ControllerQUICMode
	}
	if sub.PreferredMode != "" {
		mode = normalizeStreamMode(sub.PreferredMode, mode)
	}
	if sub.PreferredFPS > 0 {
		fps = clamp(sub.PreferredFPS, 1, 60)
	}
	if sub.PreferredQuality > 0 {
		quality = clamp(sub.PreferredQuality, 30, 100)
	}
	if sub.PreferredFullMS > 0 {
		fullMS = clamp(sub.PreferredFullMS, 500, 20000)
	}
	h264Preferred := transport == "quic" && normalizeFrameCodec(sub.PreferredCodec) == "h264"
	deltaSuggestion := transport == "quic" && !h264Preferred

	outstanding := int(sub.LastSentFrameID - sub.AckFrameID)
	if outstanding < 0 {
		outstanding = 0
	}

	switch {
	case sub.SlowScore >= 6:
		fps = minInt(fps, 14)
		quality = minInt(quality, 42)
		fullMS = maxInt(fullMS, 10000)
		if deltaSuggestion {
			mode = "delta"
		}
	case sub.SlowScore >= 4:
		fps = minInt(fps, 18)
		quality = minInt(quality, 44)
		fullMS = maxInt(fullMS, 9000)
		if deltaSuggestion {
			mode = "delta"
		}
	case sub.SlowScore >= 2:
		fps = minInt(fps, 24)
		quality = minInt(quality, 48)
		fullMS = maxInt(fullMS, 7600)
		if deltaSuggestion {
			mode = "delta"
		}
	case sub.SlowScore >= 1:
		fps = minInt(fps, 30)
		quality = minInt(quality, 50)
		fullMS = maxInt(fullMS, 6800)
	}

	switch {
	case outstanding >= 6:
		fps = minInt(fps, 14)
		quality = minInt(quality, 40)
		fullMS = maxInt(fullMS, 10500)
		if deltaSuggestion {
			mode = "delta"
		}
	case outstanding >= 4:
		fps = minInt(fps, 18)
		quality = minInt(quality, 42)
		fullMS = maxInt(fullMS, 9500)
		if deltaSuggestion {
			mode = "delta"
		}
	case outstanding >= 2:
		fps = minInt(fps, 22)
		quality = minInt(quality, 46)
		fullMS = maxInt(fullMS, 8200)
		if deltaSuggestion {
			mode = "delta"
		}
	}

	if !sub.LastResyncAt.IsZero() {
		age := now.Sub(sub.LastResyncAt)
		switch {
		case age < 2*time.Second:
			fps = minInt(fps, 12)
			quality = minInt(quality, 40)
			fullMS = maxInt(fullMS, 11000)
			if deltaSuggestion {
				mode = "delta"
			}
		case age < 6*time.Second:
			fps = minInt(fps, 18)
			quality = minInt(quality, 44)
			fullMS = maxInt(fullMS, 9000)
			if deltaSuggestion {
				mode = "delta"
			}
		}
	}

	if sub.LastSentFrameID > sub.AckFrameID && !sub.LastSentAt.IsZero() {
		if sub.LastAckAt.IsZero() {
			if now.Sub(sub.LastSentAt) > 2*time.Second {
				fps = minInt(fps, 12)
				quality = minInt(quality, 42)
				fullMS = maxInt(fullMS, 9800)
				if deltaSuggestion {
					mode = "delta"
				}
			}
		} else if now.Sub(sub.LastAckAt) > 3*time.Second {
			fps = minInt(fps, 10)
			quality = minInt(quality, 40)
			fullMS = maxInt(fullMS, 11000)
			if deltaSuggestion {
				mode = "delta"
			}
		}
	}

	targetKBps := resolveTargetKBpsLocked(sub, now, fps)
	targetBPS := float64(targetKBps) * 1024
	if targetBPS > 0 && sub.AvgFrameBytes > 0 {
		budgetFPS := int((targetBPS * 0.92) / sub.AvgFrameBytes)
		if budgetFPS > 0 {
			fps = minInt(fps, clamp(budgetFPS, 8, activeServerTuning.MaxAutoFPS))
		}
		if deltaSuggestion && sub.AvgFrameBytes*float64(maxInt(fps, 1)) > targetBPS*1.08 {
			mode = "delta"
		}
	}
	if targetBPS > 0 && sub.RecentBPS > targetBPS*1.05 {
		fps = minInt(fps, 18)
		quality = minInt(quality, 48)
		fullMS = maxInt(fullMS, 7000)
		if deltaSuggestion {
			mode = "delta"
		}
	}
	codec := "jpeg"
	if normalizeStreamMode(mode, "video") == "video" && h264Preferred {
		codec = "h264"
	}

	return normalizeStreamMode(mode, "video"), codec, clamp(fps, 8, activeServerTuning.MaxAutoFPS), clamp(quality, 40, 100), clamp(fullMS, 1800, 15000)
}

func resolveTargetKBpsLocked(sub *frameSubscriber, now time.Time, fps int) int {
	if sub == nil {
		return activeServerTuning.TargetKBps
	}
	if sub.PreferredTargetKBps > 0 {
		sub.CurrentTargetKBps = clamp(sub.PreferredTargetKBps, 128, 4096)
		return sub.CurrentTargetKBps
	}
	if activeServerTuning.TargetKBps > 0 {
		sub.CurrentTargetKBps = activeServerTuning.TargetKBps
		return activeServerTuning.TargetKBps
	}

	minKBps := clamp(activeServerTuning.AutoTargetMinKBps, 128, 4096)
	maxKBps := clamp(activeServerTuning.AutoTargetMaxKBps, minKBps, 4096)
	target := sub.AdaptiveTargetKBps
	if target <= 0 {
		target = clamp(500, minKBps, maxKBps)
		sub.AdaptiveTargetKBps = target
		sub.CurrentTargetKBps = target
		sub.AdaptiveTuneAt = now
		return target
	}

	outstanding := int(sub.LastSentFrameID - sub.AckFrameID)
	if outstanding < 0 {
		outstanding = 0
	}
	targetBPS := float64(target) * 1024
	slow := sub.SlowScore >= 2 || outstanding >= 3
	if !sub.LastResyncAt.IsZero() && now.Sub(sub.LastResyncAt) < 6*time.Second {
		slow = true
	}
	if sub.LastSentFrameID > sub.AckFrameID && !sub.LastSentAt.IsZero() {
		if sub.LastAckAt.IsZero() {
			if now.Sub(sub.LastSentAt) > 1500*time.Millisecond {
				slow = true
			}
		} else if now.Sub(sub.LastAckAt) > 1800*time.Millisecond {
			slow = true
		}
	}
	overshoot := targetBPS > 0 && sub.RecentBPS > targetBPS*1.04
	if slow || overshoot {
		if sub.AdaptiveTuneAt.IsZero() || now.Sub(sub.AdaptiveTuneAt) >= 900*time.Millisecond {
			next := int(float64(target) * 0.84)
			if sub.SlowScore >= 4 || outstanding >= 5 || (overshoot && sub.RecentBPS > targetBPS*1.15) {
				next = int(float64(target) * 0.72)
			}
			target = clamp(next, minKBps, maxKBps)
			sub.AdaptiveTargetKBps = target
			sub.AdaptiveTuneAt = now
		}
		sub.CurrentTargetKBps = sub.AdaptiveTargetKBps
		return sub.CurrentTargetKBps
	}

	ackFresh := !sub.LastAckAt.IsZero() && now.Sub(sub.LastAckAt) <= 1200*time.Millisecond
	demandBPS := sub.AvgFrameBytes * float64(maxInt(fps, 8))
	nearCap := targetBPS > 0 && (sub.RecentBPS > targetBPS*0.72 || demandBPS > targetBPS*0.82)
	if sub.SlowScore == 0 && outstanding == 0 && ackFresh && nearCap {
		if sub.AdaptiveTuneAt.IsZero() || now.Sub(sub.AdaptiveTuneAt) >= 2*time.Second {
			next := maxInt(target+64, int(float64(target)*1.12))
			target = clamp(next, minKBps, maxKBps)
			sub.AdaptiveTargetKBps = target
			sub.AdaptiveTuneAt = now
		}
	}

	sub.CurrentTargetKBps = sub.AdaptiveTargetKBps
	return sub.CurrentTargetKBps
}

func configureServerTCP(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetKeepAlivePeriod(30 * time.Second)
	}
}

func applyControllerPreferencesLocked(sub *frameSubscriber, msg wsClientMessage) bool {
	if sub == nil {
		return false
	}
	changed := false
	mode := strings.TrimSpace(strings.ToLower(msg.Mode))
	switch mode {
	case "", "auto":
		if sub.PreferredMode != "" {
			sub.PreferredMode = ""
			changed = true
		}
	case "video", "delta":
		if sub.PreferredMode != mode {
			sub.PreferredMode = mode
			changed = true
		}
	}
	fps := clamp(msg.FPS, 0, 60)
	if sub.PreferredFPS != fps {
		sub.PreferredFPS = fps
		changed = true
	}
	quality := clamp(msg.Quality, 0, 100)
	if sub.PreferredQuality != quality {
		sub.PreferredQuality = quality
		changed = true
	}
	fullMS := clamp(msg.FullMS, 0, 20000)
	if sub.PreferredFullMS != fullMS {
		sub.PreferredFullMS = fullMS
		changed = true
	}
	targetKBps := clamp(msg.TargetKBps, 0, 4096)
	if sub.PreferredTargetKBps != targetKBps {
		sub.PreferredTargetKBps = targetKBps
		changed = true
	}
	return changed
}

func (h *hub) handleControllerControlMessageLocked(dev *deviceState, sub *frameSubscriber, msg wsClientMessage, deviceID string) bool {
	if dev == nil || sub == nil {
		return false
	}
	now := time.Now()
	sub.LastPingAt = now

	switch strings.TrimSpace(strings.ToLower(msg.Type)) {
	case "ack":
		if msg.FrameID > sub.AckFrameID {
			sub.AckFrameID = msg.FrameID
		}
		sub.LastAckAt = now
		if sub.SlowScore > 0 {
			sub.SlowScore--
		}
		if dev.FrameID > sub.AckFrameID {
			notifySubscriber(sub)
		}
	case "input":
		if msg.Input != nil {
			ev := *msg.Input
			ev.DeviceID = deviceID
			ev.Type = strings.TrimSpace(ev.Type)
			if ev.Type != "" {
				enqueueInputLocked(dev, ev)
			}
		}
	case "resync":
		sub.AckFrameID = 0
		sub.LastSentFrameID = 0
		sub.LastDesktopStatus = ""
		sub.LastResyncAt = now
		sub.SlowScore = clamp(sub.SlowScore+3, 0, 8)
		notifySubscriber(sub)
	case "hello":
		sub.PreferredCodec = normalizeFrameCodec(msg.Codec)
		if applyControllerPreferencesLocked(sub, msg) {
			notifySubscriber(sub)
		}
	case "viewer_state":
		paused := msg.Paused != nil && *msg.Paused
		changed := applyControllerPreferencesLocked(sub, msg)
		if sub.Paused != paused {
			sub.Paused = paused
			changed = true
		}
		if changed {
			notifySubscriber(sub)
		}
	case "viewer_ping":
	default:
		return false
	}
	return true
}

func enqueueInputLocked(dev *deviceState, ev inputEvent) (inputEvent, bool) {
	if dev == nil {
		return ev, false
	}

	if ev.Type == "clipboard_toggle" {
		enabled := true
		if ev.ClipboardSync != nil {
			enabled = *ev.ClipboardSync
		}
		if dev.ClipboardSyncDisabled == enabled {
			dev.ClipboardSyncDisabled = !enabled
			notifyLocked(dev)
			return ev, true
		}
		return ev, false
	}

	if ev.Type == "clipboard_set" {
		if dev.ClipboardSyncDisabled {
			return ev, false
		}
		payload := clipboardState{DeviceID: dev.DeviceID, Text: ev.Text}
		if ev.Clipboard != nil {
			payload = *ev.Clipboard
			payload.DeviceID = dev.DeviceID
		}
		if clipboardPayloadKind(payload) == "files" {
			return ev, false
		}
		payload = normalizeClipboardPayload(payload)
		if len(payload.Image) > maxClipboardImageBytes {
			return ev, false
		}
		if !applyClipboardToDeviceLocked(dev, payload) {
			return ev, false
		}
		ev.Clipboard = &payload
		ev.Text = payload.Text
		notifyLocked(dev)
	}

	if ev.Type == "move" && len(dev.Queue) > 0 && dev.Queue[len(dev.Queue)-1].Type == "move" {
		dev.NextSeq++
		ev.Seq = dev.NextSeq
		dev.Queue[len(dev.Queue)-1] = ev
		return ev, true
	}

	dev.NextSeq++
	ev.Seq = dev.NextSeq
	dev.Queue = append(dev.Queue, ev)
	if len(dev.Queue) > 1000 {
		dev.Queue = append([]inputEvent(nil), dev.Queue[len(dev.Queue)-500:]...)
	}
	return ev, true
}

func (h *hub) requireAdminKey(w http.ResponseWriter, r *http.Request) bool {
	if h.adminKey == "" {
		return true
	}

	provided := strings.TrimSpace(r.URL.Query().Get("key"))
	if provided == "" {
		provided = strings.TrimSpace(r.Header.Get("X-Admin-Key"))
	}
	if provided == h.adminKey {
		return true
	}

	http.Error(w, "admin key is required", http.StatusUnauthorized)
	return false
}

func notifyLocked(dev *deviceState) {
	if dev == nil {
		return
	}
	if dev.Notify != nil {
		select {
		case dev.Notify <- struct{}{}:
		default:
		}
	}
	for sub := range dev.Subscribers {
		if sub == nil || sub.Paused {
			continue
		}
		notifySubscriber(sub)
	}
}

func notifySubscriber(sub *frameSubscriber) {
	if sub == nil || sub.Notify == nil {
		return
	}
	select {
	case sub.Notify <- struct{}{}:
	default:
	}
}

func noteUploadLocked(dev *deviceState, mode string, size int) {
	if dev == nil || size <= 0 {
		return
	}
	now := time.Now()
	if dev.UploadStatAt.IsZero() {
		dev.UploadStatAt = now
	}
	dev.UploadStatBytes += int64(size)
	dev.UploadStatFrames++
	dev.UploadStatLast = mode
	if dev.UploadAvgBytes <= 0 {
		dev.UploadAvgBytes = float64(size)
	} else {
		dev.UploadAvgBytes = dev.UploadAvgBytes*0.82 + float64(size)*0.18
	}
	elapsed := now.Sub(dev.UploadStatAt)
	if elapsed < activeTrafficLogWindow {
		return
	}
	dev.UploadFPS = float64(dev.UploadStatFrames) / elapsed.Seconds()
	dev.UploadBPS = float64(dev.UploadStatBytes) / elapsed.Seconds()
	log.Printf("[srv][uplink] device=%s fps=%.1f rate=%s/s avg=%s last=%s", dev.DeviceID, dev.UploadFPS, formatBytes(int64(dev.UploadBPS)), formatBytes(int64(dev.UploadAvgBytes)), dev.UploadStatLast)
	dev.UploadStatAt = now
	dev.UploadStatBytes = 0
	dev.UploadStatFrames = 0
	dev.UploadStatLast = ""
}

func noteDownlinkLocked(dev *deviceState, sub *frameSubscriber, size int, updateKind string) {
	if dev == nil || sub == nil || size <= 0 {
		return
	}
	now := time.Now()
	if sub.LogStatAt.IsZero() {
		sub.LogStatAt = now
	}
	sub.LogStatBytes += int64(size)
	sub.LogStatFrames++
	sub.LogStatLast = updateKind
	if sub.AvgFrameBytes <= 0 {
		sub.AvgFrameBytes = float64(size)
	} else {
		sub.AvgFrameBytes = sub.AvgFrameBytes*0.82 + float64(size)*0.18
	}
	elapsed := now.Sub(sub.LogStatAt)
	if elapsed < activeTrafficLogWindow {
		return
	}
	sub.RecentFPS = float64(sub.LogStatFrames) / elapsed.Seconds()
	sub.RecentBPS = float64(sub.LogStatBytes) / elapsed.Seconds()
	label := strings.TrimSpace(strings.ToLower(sub.Transport))
	if label == "" {
		label = "stream"
	}
	log.Printf("[srv][downlink/%s] device=%s fps=%.1f rate=%s/s avg=%s last=%s slow=%d target=%dKB/s", label, dev.DeviceID, sub.RecentFPS, formatBytes(int64(sub.RecentBPS)), formatBytes(int64(sub.AvgFrameBytes)), sub.LogStatLast, sub.SlowScore, maxInt(sub.CurrentTargetKBps, activeServerTuning.TargetKBps))
	sub.LogStatAt = now
	sub.LogStatBytes = 0
	sub.LogStatFrames = 0
	sub.LogStatLast = ""
}

func (h *hub) writeControllerFrames(conn *websocket.Conn, deviceID string, sub *frameSubscriber) error {
	for {
		select {
		case <-sub.Done:
			return nil
		case <-sub.Notify:
		}

		drainSubscriber(sub)

		packet, err := h.buildFramePacket(deviceID, sub)
		if err != nil {
			return err
		}
		if packet == nil {
			continue
		}

		if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			return err
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, packet.body); err != nil {
			return err
		}

		h.mu.Lock()
		if packet.statusOnly {
			sub.LastDesktopStatus = packet.statusKey
		}
		if !packet.cursorOnly && !packet.statusOnly {
			sub.LastSentFrameID = packet.frameID
		}
		if packet.cursorVersion > 0 {
			sub.LastSentCursorVer = packet.cursorVersion
		}
		if !packet.cursorOnly && !packet.statusOnly {
			sub.LastSentAt = time.Now()
		}
		if dev, ok := h.devices[deviceID]; ok && !packet.cursorOnly && !packet.statusOnly {
			noteDownlinkLocked(dev, sub, len(packet.body), packet.updateKind)
			if shouldContinueFramePumpLocked(dev, sub) {
				notifySubscriber(sub)
			}
		}
		h.mu.Unlock()
	}
}

func (h *hub) writeQUICFrames(conn *quic.Conn, deviceID string, sub *frameSubscriber) error {
	for {
		select {
		case <-sub.Done:
			return nil
		case <-sub.Notify:
		}

		drainSubscriber(sub)

		packet, err := h.buildFramePacket(deviceID, sub)
		if err != nil {
			return err
		}
		if packet == nil {
			continue
		}

		chunks, err := splitQUICDatagrams(packet.frameID, packet.body)
		if err != nil {
			return err
		}

		interrupted := false
		for _, chunk := range chunks {
			if err := conn.SendDatagram(chunk); err != nil {
				return err
			}
			if takeSubscriberNotify(sub) {
				drainSubscriber(sub)
				interrupted = true
				break
			}
		}
		if interrupted {
			continue
		}

		h.mu.Lock()
		if packet.statusOnly {
			sub.LastDesktopStatus = packet.statusKey
		}
		if !packet.cursorOnly && !packet.statusOnly {
			sub.LastSentFrameID = packet.frameID
		}
		if packet.cursorVersion > 0 {
			sub.LastSentCursorVer = packet.cursorVersion
		}
		if !packet.cursorOnly && !packet.statusOnly {
			sub.LastSentAt = time.Now()
		}
		if dev, ok := h.devices[deviceID]; ok && !packet.cursorOnly && !packet.statusOnly {
			noteDownlinkLocked(dev, sub, len(packet.body), packet.updateKind)
			if shouldContinueFramePumpLocked(dev, sub) {
				notifySubscriber(sub)
			}
		}
		h.mu.Unlock()
	}
}

func drainSubscriber(sub *frameSubscriber) {
	for {
		select {
		case <-sub.Notify:
		default:
			return
		}
	}
}

func takeSubscriberNotify(sub *frameSubscriber) bool {
	if sub == nil || sub.Notify == nil {
		return false
	}
	select {
	case <-sub.Notify:
		return true
	default:
		return false
	}
}

func (h *hub) buildFramePacket(deviceID string, sub *frameSubscriber) (*wsFramePacket, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	dev, ok := h.devices[deviceID]
	if !ok {
		return nil, fmt.Errorf("device not found")
	}
	if sub != nil && sub.Paused {
		return nil, nil
	}
	markViewerLocked(dev, "controller")
	if strings.TrimSpace(strings.ToLower(dev.DesktopStatus)) != "unavailable" && sub != nil {
		sub.LastDesktopStatus = ""
	}
	if strings.TrimSpace(strings.ToLower(dev.DesktopStatus)) == "unavailable" {
		statusKey := desktopStatusKeyLocked(dev)
		if sub == nil || sub.LastDesktopStatus != statusKey {
			header := wsFrameEnvelope{
				Type:       "desktop_status",
				DeviceID:   dev.DeviceID,
				Name:       dev.Name,
				Alias:      dev.Alias,
				FrameID:    dev.FrameID,
				UpdateKind: "desktop_status",
				Status:     "unavailable",
				Message:    dev.DesktopMessage,
				Width:      dev.Width,
				Height:     dev.Height,
				OriginX:    dev.OriginX,
				OriginY:    dev.OriginY,
			}
			body, err := encodeWSFramePacket(header, nil)
			if err != nil {
				return nil, err
			}
			return &wsFramePacket{
				frameID:    maxInt64(dev.FrameID, 1),
				body:       body,
				updateKind: "desktop_status",
				statusOnly: true,
				statusKey:  statusKey,
			}, nil
		}
		return nil, nil
	}
	if sub != nil && dev.CursorVersion > sub.LastSentCursorVer && (dev.FrameID == 0 || len(dev.Frame) == 0 || dev.FrameID == sub.LastSentFrameID) {
		header := wsFrameEnvelope{
			Type:       "cursor",
			DeviceID:   dev.DeviceID,
			Name:       dev.Name,
			Alias:      dev.Alias,
			FrameID:    dev.FrameID,
			UpdateKind: "cursor",
			Width:      dev.Width,
			Height:     dev.Height,
			OriginX:    dev.OriginX,
			OriginY:    dev.OriginY,
		}
		populateCursorHeaderLocked(&header, dev, true)
		body, err := encodeWSFramePacket(header, nil)
		if err != nil {
			return nil, err
		}
		return &wsFramePacket{
			frameID:       maxInt64(dev.FrameID, dev.CursorVersion),
			body:          body,
			updateKind:    "cursor",
			cursorVersion: dev.CursorVersion,
			cursorOnly:    true,
		}, nil
	}
	if dev.FrameID == 0 || len(dev.Frame) == 0 {
		return nil, nil
	}
	payload := bytes.Clone(dev.Frame)
	frameID := dev.FullFrameID
	if frameID <= 0 {
		frameID = dev.FrameID
	}
	header := wsFrameEnvelope{
		Type:       "frame",
		DeviceID:   dev.DeviceID,
		Name:       dev.Name,
		Alias:      dev.Alias,
		FrameID:    frameID,
		UpdateKind: "full",
		Width:      dev.Width,
		Height:     dev.Height,
		OriginX:    dev.OriginX,
		OriginY:    dev.OriginY,
		CursorX:    dev.CursorX,
		CursorY:    dev.CursorY,
		CursorOn:   dev.CursorVisible,
		CursorKind: dev.CursorKind,
		Codec:      normalizeFrameCodec(dev.FrameCodec),
		Quality:    dev.FrameQuality,
	}
	populateCursorHeaderLocked(&header, dev, sub == nil || dev.CursorVersion > sub.LastSentCursorVer)
	updateKind := "full"

	if update, ok := selectSubscriberIncrementalLocked(dev, sub); ok {
		frameID = update.FrameID
		payload = bytes.Clone(update.Payload)
		updateKind = update.UpdateKind
		header.FrameID = update.FrameID
		header.UpdateKind = update.UpdateKind
		header.BaseFrameID = update.BaseFrameID
		header.Codec = normalizeFrameCodec(update.Codec)
		header.Quality = update.Quality
		switch update.UpdateKind {
		case "patch":
			header.PatchX = update.PatchX
			header.PatchY = update.PatchY
			header.PatchW = update.PatchW
			header.PatchH = update.PatchH
		case "tiles":
			header.Tiles = cloneTiles(update.Tiles)
		}
	} else {
		if shouldHoldFullFallback(sub) {
			return nil, nil
		}
		if frameID <= sub.LastSentFrameID {
			return nil, nil
		}
	}

	header.PayloadSize = len(payload)
	body, err := encodeWSFramePacket(header, payload)
	if err != nil {
		return nil, err
	}
	return &wsFramePacket{
		frameID:       frameID,
		body:          body,
		updateKind:    updateKind,
		cursorVersion: header.CursorVer,
	}, nil
}

func populateCursorHeaderLocked(header *wsFrameEnvelope, dev *deviceState, includeImage bool) {
	if header == nil || dev == nil {
		return
	}
	header.CursorX = dev.CursorX
	header.CursorY = dev.CursorY
	header.CursorOn = dev.CursorVisible
	header.CursorKind = dev.CursorKind
	header.CursorVer = dev.CursorVersion
}

func desktopStatusKeyLocked(dev *deviceState) string {
	if dev == nil {
		return ""
	}
	return strings.TrimSpace(strings.ToLower(dev.DesktopStatus)) + "|" + strings.TrimSpace(dev.DesktopMessage)
}

func encodeWSFramePacket(header wsFrameEnvelope, payload []byte) ([]byte, error) {
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return nil, err
	}
	if uint64(len(headerJSON)) > uint64(^uint32(0)) {
		return nil, errors.New("frame header too large")
	}
	packet := make([]byte, 4+len(headerJSON)+len(payload))
	packet[0] = byte(len(headerJSON) >> 24)
	packet[1] = byte(len(headerJSON) >> 16)
	packet[2] = byte(len(headerJSON) >> 8)
	packet[3] = byte(len(headerJSON))
	copy(packet[4:], headerJSON)
	copy(packet[4+len(headerJSON):], payload)
	return packet, nil
}

func decodeAgentFramePacket(data []byte) (agentFrameEnvelope, []byte, error) {
	var header agentFrameEnvelope
	if len(data) < 4 {
		return header, nil, fmt.Errorf("frame packet is too short")
	}
	headerLen := int(uint32(data[0])<<24 | uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3]))
	if headerLen <= 0 || 4+headerLen > len(data) {
		return header, nil, fmt.Errorf("frame header size is invalid")
	}
	if err := json.Unmarshal(data[4:4+headerLen], &header); err != nil {
		return header, nil, err
	}
	payload := append([]byte(nil), data[4+headerLen:]...)
	if header.PayloadSize > 0 && len(payload) != header.PayloadSize {
		return header, nil, fmt.Errorf("frame payload size mismatch")
	}
	return header, payload, nil
}

func splitQUICDatagrams(frameID int64, packet []byte) ([][]byte, error) {
	return splitQUICDatagramsWithMagic(quicDatagramMagicDownlink, frameID, packet)
}

func splitQUICDatagramsWithMagic(magic byte, frameID int64, packet []byte) ([][]byte, error) {
	if len(packet) == 0 {
		return nil, nil
	}
	chunkCount := (len(packet) + quicDatagramChunkPayload - 1) / quicDatagramChunkPayload
	if chunkCount <= 0 || chunkCount > 0xFFFF {
		return nil, errors.New("frame is too large for quic datagrams")
	}

	chunks := make([][]byte, 0, chunkCount)
	for index := 0; index < chunkCount; index++ {
		start := index * quicDatagramChunkPayload
		end := start + quicDatagramChunkPayload
		if end > len(packet) {
			end = len(packet)
		}

		datagram := make([]byte, quicDatagramHeaderSize+(end-start))
		datagram[0] = magic
		putUint64(datagram[1:9], uint64(frameID))
		putUint16(datagram[9:11], uint16(index))
		putUint16(datagram[11:13], uint16(chunkCount))
		copy(datagram[quicDatagramHeaderSize:], packet[start:end])
		chunks = append(chunks, datagram)
	}
	return chunks, nil
}

type quicDatagramAssembler struct {
	frameID     int64
	chunkCount  int
	chunks      [][]byte
	received    []bool
	receivedCnt int
}

func (a *quicDatagramAssembler) Push(datagram []byte, magic byte) ([]byte, bool, error) {
	if len(datagram) < quicDatagramHeaderSize || datagram[0] != magic {
		return nil, false, fmt.Errorf("invalid quic datagram")
	}

	frameID := int64(readUint64(datagram[1:9]))
	chunkIndex := int(readUint16(datagram[9:11]))
	chunkCount := int(readUint16(datagram[11:13]))
	if chunkCount <= 0 || chunkIndex < 0 || chunkIndex >= chunkCount {
		return nil, false, fmt.Errorf("invalid quic chunk index")
	}

	if frameID < a.frameID {
		return nil, false, nil
	}
	if frameID != a.frameID || chunkCount != a.chunkCount {
		a.frameID = frameID
		a.chunkCount = chunkCount
		a.chunks = make([][]byte, chunkCount)
		a.received = make([]bool, chunkCount)
		a.receivedCnt = 0
	}

	if !a.received[chunkIndex] {
		a.received[chunkIndex] = true
		a.chunks[chunkIndex] = append([]byte(nil), datagram[quicDatagramHeaderSize:]...)
		a.receivedCnt++
	}
	if a.receivedCnt != a.chunkCount {
		return nil, false, nil
	}

	totalSize := 0
	for _, chunk := range a.chunks {
		totalSize += len(chunk)
	}
	packet := make([]byte, 0, totalSize)
	for _, chunk := range a.chunks {
		packet = append(packet, chunk...)
	}

	a.frameID = 0
	a.chunkCount = 0
	a.chunks = nil
	a.received = nil
	a.receivedCnt = 0
	return packet, true, nil
}

func randomKey(size int) string {
	if size <= 0 {
		size = 16
	}
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "admin-key"
	}
	return hex.EncodeToString(buf)
}

func generateQUICServerTLSConfig() (*tls.Config, error) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}

	serialNumber, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		return nil, err
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: "remote-desktop-quic",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
	}

	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, privateKey.Public(), privateKey)
	if err != nil {
		return nil, err
	}

	certificate := tls.Certificate{
		Certificate: [][]byte{certificateDER},
		PrivateKey:  privateKey,
	}

	return &tls.Config{
		Certificates: []tls.Certificate{certificate},
		NextProtos:   []string{quicNextProto},
	}, nil
}

func displayListenURL(addr string) string {
	addr = strings.TrimSpace(addr)
	switch {
	case addr == "":
		return "http://0.0.0.0:8080"
	case strings.HasPrefix(addr, ":"):
		return "http://0.0.0.0" + addr
	case strings.HasPrefix(addr, "0.0.0.0:"),
		strings.HasPrefix(addr, "127.0.0.1:"),
		strings.HasPrefix(addr, "localhost:"):
		return "http://" + addr
	default:
		return "http://" + addr
	}
}

func displayListenHost(addr string) string {
	addr = strings.TrimSpace(addr)
	switch {
	case addr == "":
		return "0.0.0.0:8080"
	case strings.HasPrefix(addr, ":"):
		return "0.0.0.0" + addr
	default:
		return addr
	}
}

func putUint16(dst []byte, value uint16) {
	dst[0] = byte(value >> 8)
	dst[1] = byte(value)
}

func readUint16(src []byte) uint16 {
	return uint16(src[0])<<8 | uint16(src[1])
}

func putUint64(dst []byte, value uint64) {
	dst[0] = byte(value >> 56)
	dst[1] = byte(value >> 48)
	dst[2] = byte(value >> 40)
	dst[3] = byte(value >> 32)
	dst[4] = byte(value >> 24)
	dst[5] = byte(value >> 16)
	dst[6] = byte(value >> 8)
	dst[7] = byte(value)
}

func readUint64(src []byte) uint64 {
	return uint64(src[0])<<56 |
		uint64(src[1])<<48 |
		uint64(src[2])<<40 |
		uint64(src[3])<<32 |
		uint64(src[4])<<24 |
		uint64(src[5])<<16 |
		uint64(src[6])<<8 |
		uint64(src[7])
}

func normalizeStreamMode(value, fallback string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "video":
		return "video"
	case "delta":
		return "delta"
	default:
		switch strings.TrimSpace(strings.ToLower(fallback)) {
		case "video":
			return "video"
		default:
			return "delta"
		}
	}
}

func normalizeFrameCodec(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "jpeg", "mjpeg":
		return "jpeg"
	case "bgra", "raw-bgra", "raw":
		return "bgra"
	case "h264", "h264-mf":
		return "h264"
	default:
		return "jpeg"
	}
}
