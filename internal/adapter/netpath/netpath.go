// Package netpath는 외부 요청이 나가는 경로를 고릅니다.
// 직접 연결이 막히면 우회 방법을 차례로 시도하고 통한 것을 기억합니다.
package netpath

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"saucedust/internal/domain"
)

type Options struct {
	Order       []domain.NetMode
	ProxyURL    string
	DoHEndpoint string
	// FragmentParts는 ClientHello를 몇 조각으로 나눌지입니다.
	// 둘로만 나누면 조각을 다시 합쳐 보는 검사 장비에 통하지 않습니다.
	FragmentParts int
	// FragmentDelay는 조각 사이 지연입니다. 짧게 두면 각 조각이 별도
	// TCP 세그먼트로 나갈 확률이 높아집니다. 0이면 바로 이어 보냅니다.
	FragmentDelay  time.Duration
	DialTimeout    time.Duration
	RequestTimeout time.Duration
	Logger         *slog.Logger
	Recorder       ProbeRecorder
	// AllowPrivate를 켜면 사설 주소로도 나갑니다.
	// 수집 대상이 알려준 주소를 그대로 받아오므로 기본은 막습니다.
	// 사내 미러를 쓰는 경우에만 켜십시오.
	AllowPrivate bool
}

// ProbeRecorder는 경로별 측정 결과를 남깁니다. 없어도 동작합니다.
type ProbeRecorder func(ctx context.Context, host string, mode domain.NetMode, ok bool, latency time.Duration, detail string)

type ProbeResult struct {
	Host    string
	Mode    domain.NetMode
	OK      bool
	Latency time.Duration
	Detail  string
	// Status는 응답을 받았을 때의 HTTP 상태입니다. 0이면 응답 자체가 없었습니다.
	Status int
	// Blocked는 연결이 중간에 끊긴 경우입니다. 차단 장비의 전형적인 모습입니다.
	Blocked bool
	// Bytes는 실제로 받은 본문 크기입니다. 이미지처럼 큰 응답에서는
	// 연결 시간보다 전송 속도가 중요하므로 함께 잽니다.
	Bytes int64
	// Proto는 협상된 HTTP 버전입니다. 우회 경로는 HTTP/1.1로 내려갑니다.
	Proto string
}

// Throughput은 초당 몇 메가바이트를 받았는지입니다. 작은 응답에서는 0입니다.
func (r ProbeResult) Throughput() float64 {
	if r.Bytes < 64*1024 || r.Latency <= 0 {
		return 0
	}
	return float64(r.Bytes) / r.Latency.Seconds() / (1024 * 1024)
}

// Verdict는 결과를 사람이 읽을 말로 바꿉니다.
func (r ProbeResult) Verdict() string {
	switch {
	case r.Blocked:
		return "차단됨"
	case r.Status == 0:
		return "실패"
	case r.Status >= 400:
		return fmt.Sprintf("도달함 %d", r.Status)
	default:
		return "성공"
	}
}

// blockSignals는 검사 장비가 끼어들었을 때 나타나는 오류들입니다.
// 서버가 없거나 이름을 못 찾는 것과 구분하기 위해 따로 봅니다.
var blockSignals = []string{
	"connection reset by peer",
	"read: connection reset",
	"broken pipe",
	"unexpected EOF",
	"EOF",
	"connection refused by",
}

func looksBlocked(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, signal := range blockSignals {
		if strings.Contains(msg, signal) {
			return true
		}
	}
	return false
}

// Chain은 호스트마다 쓸 수 있는 가장 빠른 경로를 골라 씁니다.
// 요청이 네트워크 문제로 실패하면 다음 경로로 내려가고, 성공한 경로를 기억합니다.
type Chain struct {
	order    []domain.NetMode
	clients  map[domain.NetMode]*http.Client
	log      *slog.Logger
	recorder ProbeRecorder
	timeout  time.Duration

	mu       sync.RWMutex
	chosen   map[string]domain.NetMode
	lastMode domain.NetMode
}

func New(opts Options) (*Chain, error) {
	if len(opts.Order) == 0 {
		return nil, errors.New("네트워크 경로 순서가 비어 있습니다")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 15 * time.Second
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = 60 * time.Second
	}
	if opts.FragmentDelay < 0 {
		opts.FragmentDelay = 0
	}
	if opts.FragmentParts < 2 {
		opts.FragmentParts = defaultFragmentParts
	}

	resolver := newECHResolver(opts.DoHEndpoint, time.Hour)
	clients := make(map[domain.NetMode]*http.Client, len(opts.Order))
	order := make([]domain.NetMode, 0, len(opts.Order))

	for _, mode := range opts.Order {
		transport, err := buildTransport(mode, opts, resolver)
		if err != nil {
			opts.Logger.Warn("경로를 준비하지 못해 건너뜁니다",
				slog.String("mode", string(mode)), slog.String("error", err.Error()))
			continue
		}
		clients[mode] = &http.Client{Transport: transport, Timeout: opts.RequestTimeout}
		order = append(order, mode)
	}
	if len(order) == 0 {
		return nil, errors.New("사용할 수 있는 네트워크 경로가 없습니다")
	}

	return &Chain{
		order:    order,
		clients:  clients,
		log:      opts.Logger,
		recorder: opts.Recorder,
		timeout:  opts.RequestTimeout,
		chosen:   map[string]domain.NetMode{},
		lastMode: order[0],
	}, nil
}

func (c *Chain) Order() []domain.NetMode { return c.order }

// Prime은 지난 실행에서 알아낸 경로를 미리 채워 넣습니다.
//
// 이것이 없으면 켤 때마다 막힌 경로부터 다시 시도합니다. 차단된 호스트는
// 연결이 끊길 때까지 기다려야 하므로 그만큼 첫 요청이 늦어집니다.
// 준비되지 않은 경로가 섞여 있으면 조용히 건너뜁니다.
func (c *Chain) Prime(known map[string]domain.NetMode) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	var applied int
	for host, mode := range known {
		if _, ok := c.clients[mode]; !ok {
			continue
		}
		c.chosen[host] = mode
		applied++
	}
	return applied
}

// Mode는 가장 최근에 성공한 경로입니다. 대시보드 표시에 씁니다.
func (c *Chain) Mode() domain.NetMode {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastMode
}

func (c *Chain) ModeFor(host string) domain.NetMode {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if mode, ok := c.chosen[host]; ok {
		return mode
	}
	return c.order[0]
}

// Do는 기억해 둔 경로부터 시작해 성공할 때까지 아래로 내려갑니다.
// 응답이 왔는데 상태 코드만 나쁜 경우는 경로 문제가 아니므로 그대로 돌려줍니다.
func (c *Chain) Do(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	var lastErr error

	for _, mode := range c.candidates(host) {
		client, ok := c.clients[mode]
		if !ok {
			continue
		}

		attempt, err := cloneRequest(req)
		if err != nil {
			return nil, err
		}

		start := time.Now()
		// 대상 주소는 수집 사이트가 알려준 것이라 그대로 믿지 않습니다.
		// dialGuard가 연결 직전에 실제 IP를 보고 내부 주소를 막습니다.
		// 이름 조회를 나중에 바꿔치기하는 수법도 그 시점에 걸립니다.
		resp, err := client.Do(attempt) // #nosec G704 -- dialGuard가 내부 주소를 차단합니다
		if err == nil {
			c.remember(host, mode)
			c.record(req.Context(), host, mode, true, time.Since(start), "")
			return resp, nil
		}
		if req.Context().Err() != nil {
			return nil, err
		}

		lastErr = err
		c.record(req.Context(), host, mode, false, time.Since(start), err.Error())
		c.log.Debug("경로 실패, 다음으로 넘어갑니다",
			slog.String("host", host), slog.String("mode", string(mode)),
			slog.String("error", err.Error()))
	}

	return nil, fmt.Errorf("모든 네트워크 경로가 실패했습니다 (%s): %w", host, lastErr)
}

// candidates는 기억해 둔 경로를 맨 앞에 두고 나머지를 원래 순서대로 붙입니다.
func (c *Chain) candidates(host string) []domain.NetMode {
	preferred := c.ModeFor(host)
	out := make([]domain.NetMode, 0, len(c.order))
	out = append(out, preferred)
	for _, mode := range c.order {
		if mode != preferred {
			out = append(out, mode)
		}
	}
	return out
}

func (c *Chain) remember(host string, mode domain.NetMode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.chosen[host] = mode
	c.lastMode = mode
}

func (c *Chain) record(ctx context.Context, host string, mode domain.NetMode, ok bool, latency time.Duration, detail string) {
	if c.recorder == nil {
		return
	}
	if len(detail) > 300 {
		detail = detail[:300]
	}
	c.recorder(ctx, host, mode, ok, latency, detail)
}

// Probe는 호스트 하나에 대해 모든 경로를 시험합니다.
// 한 번만 재면 회선 흔들림과 실제 차이를 구분할 수 없으므로 여러 번 재서
// 중앙값을 씁니다. 가장 빠르게 통한 경로를 이후 기본값으로 기억합니다.
func (c *Chain) Probe(ctx context.Context, target string, samples int) ([]ProbeResult, error) {
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("측정 대상 주소가 잘못되었습니다: %w", err)
	}
	if samples < 1 {
		samples = 3
	}
	host := parsed.Hostname()

	results := make([]ProbeResult, 0, len(c.order))
	best := ProbeResult{Latency: time.Duration(1<<63 - 1)}

	for _, mode := range c.order {
		res := c.probeMode(ctx, host, target, mode, samples)
		c.record(ctx, host, mode, res.OK, res.Latency, res.Detail)
		results = append(results, res)

		if res.OK && res.Latency < best.Latency {
			best = res
		}
	}

	if best.OK {
		c.remember(host, best.Mode)
	}
	return results, nil
}

func (c *Chain) probeMode(ctx context.Context, host, target string, mode domain.NetMode, samples int) ProbeResult {
	client := c.clients[mode]
	out := ProbeResult{Host: host, Mode: mode}

	took := make([]time.Duration, 0, samples)
	for i := 0; i < samples; i++ {
		if ctx.Err() != nil {
			break
		}

		attempt, err := c.probeOnce(ctx, client, target)
		if err != nil {
			out.Detail = err.Error()
			out.Blocked = looksBlocked(err)
			// 한 번이라도 실패하면 그 경로는 믿을 수 없습니다.
			return out
		}

		out.Status = attempt.status
		out.Proto = attempt.proto
		out.Bytes = attempt.bytes
		out.Detail = attempt.detail
		took = append(took, attempt.took)
	}

	if len(took) == 0 {
		return out
	}
	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
	out.Latency = took[len(took)/2]
	// 응답이 왔다면 경로는 뚫린 것입니다. 상태 코드는 응용 계층 문제입니다.
	out.OK = out.Status < 500
	return out
}

type probeAttempt struct {
	status int
	proto  string
	bytes  int64
	detail string
	took   time.Duration
}

// probeOnce는 한 번 받아옵니다. 본문을 끝까지 읽어야 경로별 전송 속도를
// 비교할 수 있으므로, 다 읽기 전에는 컨텍스트를 취소하지 않습니다.
func (c *Chain) probeOnce(ctx context.Context, client *http.Client, target string) (probeAttempt, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, target, nil)
	if err != nil {
		return probeAttempt{}, err
	}
	req.Header.Set("user-agent", probeUserAgent)

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return probeAttempt{}, err
	}
	defer resp.Body.Close()

	read, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxProbeBody))
	if err != nil {
		return probeAttempt{}, err
	}

	return probeAttempt{
		status: resp.StatusCode,
		proto:  resp.Proto,
		bytes:  read,
		detail: resp.Status,
		took:   time.Since(start),
	}, nil
}

const (
	// defaultFragmentParts는 실측으로 정했습니다. 값을 올려도 통과율은 같고
	// 왕복만 늘어납니다.
	defaultFragmentParts = 3

	probeUserAgent = "saucedust-probe/0.2"
	// 큰 이미지도 끝까지 받아 전송 속도를 비교합니다.
	maxProbeBody = 16 << 20
)

func buildTransport(mode domain.NetMode, opts Options, resolver *echResolver) (http.RoundTripper, error) {
	dialer := &net.Dialer{
		Timeout:   opts.DialTimeout,
		KeepAlive: 30 * time.Second,
		Control:   dialGuard(opts.AllowPrivate),
	}

	base := &http.Transport{
		DialContext:           dialer.DialContext,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   opts.DialTimeout,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	}

	switch mode {
	case domain.NetDirect:
		return base, nil

	case domain.NetProxy:
		if opts.ProxyURL == "" {
			return nil, errors.New("프록시 주소가 설정되지 않았습니다")
		}
		proxy, err := url.Parse(opts.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("프록시 주소가 잘못되었습니다: %w", err)
		}
		base.Proxy = http.ProxyURL(proxy)
		return base, nil

	case domain.NetFragment:
		// ForceAttemptHTTP2를 켜 두면 직접 TLS를 물려도 h2를 협상합니다.
		// 예전에는 http/1.1로 내려버려서 API 호출이 다중화 이득을 못 봤습니다.
		base.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			raw, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}

			conn := tls.Client(&fragmentConn{
				Conn:  raw,
				delay: opts.FragmentDelay,
				parts: opts.FragmentParts,
			}, &tls.Config{
				ServerName: host,
				MinVersion: tls.VersionTLS12,
				NextProtos: []string{"h2", "http/1.1"},
			})
			if err := conn.HandshakeContext(ctx); err != nil {
				// 이미 실패한 연결을 닫는 것이라 그 결과는 볼 것이 없습니다.
				_ = raw.Close()
				return nil, err
			}
			return conn, nil
		}
		return base, nil

	case domain.NetECH:
		base.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			echConfig, err := resolver.ConfigFor(ctx, host)
			if err != nil {
				return nil, err
			}
			raw, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			conn := tls.Client(raw, &tls.Config{
				ServerName:                     host,
				MinVersion:                     tls.VersionTLS13,
				NextProtos:                     []string{"http/1.1"},
				EncryptedClientHelloConfigList: echConfig,
			})
			if err := conn.HandshakeContext(ctx); err != nil {
				// 이미 실패한 연결을 닫는 것이라 그 결과는 볼 것이 없습니다.
				_ = raw.Close()
				return nil, err
			}
			return conn, nil
		}
		return base, nil

	default:
		return nil, fmt.Errorf("알 수 없는 네트워크 경로입니다: %s", mode)
	}
}

func cloneRequest(req *http.Request) (*http.Request, error) {
	clone := req.Clone(req.Context())
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		clone.Body = body
	}
	return clone, nil
}
