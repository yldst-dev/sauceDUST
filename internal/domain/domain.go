// Package domain은 이 시스템의 개념과 규칙을 담습니다.
// 저장소도 네트워크도 모르며, 바깥 계층 어디에도 의존하지 않습니다.
package domain

import (
	"errors"
	"time"
)

var (
	ErrNotFound      = errors.New("찾지 못했습니다")
	ErrLeaseConflict = errors.New("임대가 이미 회수되었습니다")
	ErrModelMismatch = errors.New("임베딩 모델이 일치하지 않습니다")
	// ErrBadVector는 값 자체가 망가진 벡터입니다. 모델 구성은 맞지만
	// 계산 결과를 쓸 수 없는 경우이므로 모델 불일치와 구분합니다.
	ErrBadVector = errors.New("벡터 값이 잘못되었습니다")
	// ErrNoWork는 지금 배정할 작업이 없다는 뜻입니다. 오류가 아니라 정상 상태이며,
	// 호출자는 잠시 쉬었다가 다시 물어보면 됩니다.
	ErrNoWork = errors.New("배정할 작업이 없습니다")
)

type Role string

const (
	RoleControl Role = "control"
	RoleWorker  Role = "worker"
)

func (r Role) Valid() bool {
	return r == RoleControl || r == RoleWorker
}

type Device string

const (
	DeviceCUDA    Device = "cuda"
	DeviceMPS     Device = "mps"
	DeviceCPU     Device = "cpu"
	DeviceUnknown Device = "unknown"
)

// NetMode는 외부 요청이 나가는 경로입니다. 앞쪽일수록 빠르고 뒤쪽일수록 확실합니다.
type NetMode string

const (
	// NetDirect는 아무 우회 없이 그대로 나갑니다.
	NetDirect NetMode = "direct"
	// NetECH는 TLS 인사말의 도메인 이름을 암호화합니다. 상대 서버 지원이 필요합니다.
	NetECH NetMode = "ech"
	// NetFragment는 TLS 인사말을 두 조각으로 나눠 보냅니다. 서버 지원은 필요 없지만
	// 조각을 다시 합치는 검사 장비에는 통하지 않습니다.
	NetFragment NetMode = "frag"
	// NetProxy는 VPN 프록시를 거칩니다. 느리지만 가장 확실합니다.
	NetProxy   NetMode = "vpn"
	NetUnknown NetMode = "unknown"
)

func (m NetMode) Valid() bool {
	switch m {
	case NetDirect, NetECH, NetFragment, NetProxy:
		return true
	}
	return false
}

type ModelKind string

const (
	ModelCopy     ModelKind = "copy"
	ModelSemantic ModelKind = "semantic"
)

type Direction string

const (
	DirectionCatchup  Direction = "catchup"
	DirectionBackfill Direction = "backfill"
)

type RangeStatus string

const (
	RangeRunning   RangeStatus = "running"
	RangeCompleted RangeStatus = "completed"
	RangeFailed    RangeStatus = "failed"
	RangeEmpty     RangeStatus = "empty"
)

type RetryStatus string

const (
	RetryPending   RetryStatus = "pending"
	RetryRunning   RetryStatus = "running"
	RetrySucceeded RetryStatus = "succeeded"
	RetryDead      RetryStatus = "dead"
)

type Node struct {
	ID          string
	Role        Role
	Hostname    string
	Platform    string
	Version     string
	Device      Device
	NetMode     NetMode
	Concurrency int
	Status      string
	StartedAt   time.Time
	HeartbeatAt time.Time
}

type EmbeddingModel struct {
	ID         string
	Kind       ModelKind
	Backend    string
	Checkpoint string
	VectorSize int
	Distance   string
	Collection string
	InputSize  int
	Active     bool
}

type Image struct {
	ID           int64
	SourceSite   string
	SourcePostID int64
	SourceURL    string
	CanonicalURL string
	FileURL      string
	PreviewURL   string
	MD5          string
	PHash        string
	DHash        string
	Width        int
	Height       int
	FileSize     int64
	Rating       string
	Score        int
	Tags         []string
	ArtistTags   []string
	ThumbPath    string
	IndexedBy    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Vector struct {
	ModelID string
	Values  []float32
}

// ThumbRef는 다시 계산할 이미지 한 건입니다.
// 모델을 바꿨을 때 원본 대신 축소본으로 벡터를 다시 만드는 데 씁니다.
type ThumbRef struct {
	ImageID      int64
	SourceSite   string
	SourcePostID int64
	ThumbPath    string
}

type IndexedImage struct {
	Image   Image
	Vectors []Vector
	Thumb   []byte
}

type CrawlRange struct {
	ID         int64
	SourceSite string
	ScopeKey   string
	Direction  Direction
	LowerID    int64
	UpperID    int64
	Attempts   int
	NodeID     string
	LeasedAt   time.Time
	LastError  string
}

func (r CrawlRange) Size() int64 { return r.UpperID - r.LowerID + 1 }

// LeaseRequest는 노드가 백필 구간을 요청할 때 쓰는 값입니다.
// RangeSize는 노드 처리 속도에 맞춰 호출자가 정합니다. 느린 노드가 큰 구간을
// 오래 붙잡지 않도록 하기 위해서입니다.
type LeaseRequest struct {
	SourceSite string
	ScopeKey   string
	NodeID     string
	RangeSize  int64
	FloorID    int64
}

type PostRetry struct {
	ID           int64
	SourceSite   string
	ScopeKey     string
	SourcePostID int64
	Attempts     int
}

type NodeMetrics struct {
	NodeID      string
	ObservedAt  time.Time
	CPUPct      float32
	MemMB       float32
	DevicePct   float32
	Concurrency int
	NetMode     NetMode
	Downloaded  int64
	Embedded    int64
	Saved       int64
	Failed      int64
}

type SearchHit struct {
	Score   float32
	ModelID string
	Image   Image
}
