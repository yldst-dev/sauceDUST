package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// cmdInstall은 빈 리눅스 상자를 도는 노드로 만듭니다.
//
// setup은 Python 쪽만 봅니다. 나머지, 곧 꾸러미 설치와 PostgreSQL 만들기와
// 토큰 만들기와 systemd 등록은 손으로 해야 했습니다. 그 손일이 스무 단계쯤
// 되고, 한 군데만 빠뜨려도 몇 분 뒤에 엉뚱한 오류로 나타납니다.
//
// 여기서 다 합니다. 사용자가 정할 것은 역할 하나입니다.
func cmdInstall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	role := fs.String("role", "", "control 또는 worker")
	prefix := fs.String("prefix", "/opt/saucedust", "설치할 폴더")
	account := fs.String("user", "saucedust", "서비스를 돌릴 계정")
	thumbDir := fs.String("thumb-dir", "", "축소본을 둘 폴더. 비우면 설치 폴더 아래")
	controlURL := fs.String("control-url", "", "worker 역할일 때 중앙 주소")
	token := fs.String("token", "", "worker 역할일 때 중앙과 같은 토큰")
	dbPassword := fs.String("db-password", "", "worker 역할일 때 중앙의 데이터베이스 비밀번호")
	join := fs.String("join", "", "중앙이 알려 준 값 하나로 위 셋을 한꺼번에 정합니다")
	bind := fs.String("bind", "", "control 역할일 때 열 주소. 비우면 127.0.0.1:8000")
	allowFrom := fs.String("allow-from", "", "PostgreSQL에 붙게 허용할 대역. 예: 10.0.0.0/24")
	yes := fs.Bool("yes", false, "물어보지 않고 진행합니다")
	skipModels := fs.Bool("skip-models", false, "모델 가중치 내려받기를 건너뜁니다")
	if err := fs.Parse(args); err != nil {
		return err
	}

	plan, err := newInstallPlan(installOptions{
		role: *role, prefix: *prefix, account: *account, thumbDir: *thumbDir,
		controlURL: *controlURL, token: *token, dbPassword: *dbPassword,
		bind: *bind, allowFrom: *allowFrom, join: *join,
	})
	if err != nil {
		return err
	}
	plan.skipModels = *skipModels

	if runtime.GOOS != "linux" {
		return fmt.Errorf("install은 리눅스에서만 씁니다. %s에서는 setup을 쓰십시오", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		self, _ := os.Executable()
		return fmt.Errorf("꾸러미를 깔고 서비스를 등록하므로 root로 돌려야 합니다:\n  sudo %s install %s",
			self, strings.Join(args, " "))
	}
	if plan.family == "" {
		return errors.New("배포판을 알아보지 못했습니다. /etc/os-release를 볼 수 없거나 아는 갈래가 아닙니다")
	}

	plan.describe()
	if !*yes && !confirm() {
		return errors.New("멈췄습니다")
	}

	return plan.apply(ctx)
}

// installPlan은 무엇을 할지 미리 정해 둔 것입니다.
//
// 정하기와 하기를 나눕니다. 그래야 하기 전에 사람에게 보여 줄 수 있고,
// 정하는 쪽만 따로 시험할 수 있습니다.
type installPlan struct {
	role       string
	family     string
	prefix     string
	account    string
	thumbDir   string
	indexDir   string
	dataDir    string
	controlURL string
	token      string
	bind       string
	allowFrom  string
	dbPassword string
	nodeID     string
	skipModels bool
	// withServer는 PostgreSQL 서버를 이 상자에 두는지입니다.
	withServer bool
}

// installOptions는 명령줄에서 온 값입니다.
type installOptions struct {
	role       string
	prefix     string
	account    string
	thumbDir   string
	controlURL string
	token      string
	dbPassword string
	bind       string
	allowFrom  string
	join       string
}

func newInstallPlan(opts installOptions) (*installPlan, error) {
	if opts.join != "" {
		ticket, err := decodeJoinTicket(opts.join)
		if err != nil {
			return nil, err
		}
		// 따로 적은 값이 있으면 그것을 남깁니다. 표를 덮지 않습니다.
		if opts.controlURL == "" {
			opts.controlURL = ticket.ControlURL
		}
		if opts.token == "" {
			opts.token = ticket.Token
		}
		if opts.dbPassword == "" {
			opts.dbPassword = ticket.DBPassword
		}
	}

	role, prefix, account := opts.role, opts.prefix, opts.account
	thumbDir, controlURL, token := opts.thumbDir, opts.controlURL, opts.token
	bind, allowFrom := opts.bind, opts.allowFrom

	switch role {
	case "control", "worker":
	case "":
		return nil, errors.New("역할을 정하십시오: -role control 또는 -role worker")
	default:
		return nil, fmt.Errorf("역할이 control이나 worker여야 합니다: %s", role)
	}

	if !filepath.IsAbs(prefix) {
		return nil, fmt.Errorf("설치 폴더는 절대 경로여야 합니다: %s", prefix)
	}
	if err := checkAccountName(account); err != nil {
		return nil, err
	}
	if allowFrom != "" {
		if _, _, err := net.ParseCIDR(allowFrom); err != nil {
			return nil, fmt.Errorf("-allow-from이 대역 표기가 아닙니다: %s", allowFrom)
		}
	}

	plan := &installPlan{
		role:       role,
		family:     currentLinuxFamily(),
		prefix:     filepath.Clean(prefix),
		account:    account,
		controlURL: controlURL,
		token:      token,
		bind:       bind,
		allowFrom:  allowFrom,
		withServer: role == "control",
	}
	plan.dataDir = filepath.Join(plan.prefix, "data")
	plan.indexDir = filepath.Join(plan.prefix, "index")
	plan.thumbDir = thumbDir
	if plan.thumbDir == "" {
		plan.thumbDir = filepath.Join(plan.prefix, "thumbs")
	}

	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = role
	}
	plan.nodeID = sanitizeNodeID(host)

	if role == "worker" {
		if strings.TrimSpace(controlURL) == "" {
			return nil, errors.New("수집 노드는 중앙 주소가 필요합니다: -control-url http://중앙:8000")
		}
		// 토큰은 중앙에서 만든 것을 그대로 받아야 합니다. 여기서 새로
		// 만들면 중앙과 달라져서 모든 요청이 401로 거절됩니다.
		if len(strings.TrimSpace(token)) < minTokenLen {
			return nil, fmt.Errorf("수집 노드는 중앙과 같은 토큰이 필요합니다. %d자 이상이어야 합니다: -token ...", minTokenLen)
		}
		// 구간을 빌리려면 PostgreSQL에 직접 붙습니다. 비밀번호가 없으면
		// 설치는 성공했다고 나오고 서비스만 붙지 못합니다. 중앙이 내주는
		// -join 값에 함께 들어 있으므로 보통은 따로 적을 일이 없습니다.
		plan.dbPassword = strings.TrimSpace(opts.dbPassword)
		if plan.dbPassword == "" {
			return nil, errors.New(
				"수집 노드는 중앙의 데이터베이스 비밀번호가 필요합니다. 중앙이 알려 준 -join 값을 그대로 쓰십시오")
		}
		return plan, nil
	}

	if plan.bind == "" {
		plan.bind = "127.0.0.1:8000"
	}
	host, _, splitErr := net.SplitHostPort(plan.bind)
	if splitErr != nil {
		return nil, fmt.Errorf("-bind가 주소:포트 표기가 아닙니다: %s", plan.bind)
	}
	// 되돌아오는 주소에 붙여 두고 다른 대역을 열면, PostgreSQL이 그 대역에서
	// 오는 것을 받아들이도록 적어 두기만 하고 실제로는 그 주소에서 듣지
	// 않습니다. 설치는 성공하고 수집 노드만 붙지 못합니다.
	if plan.allowFrom != "" && isLoopbackHost(host) {
		return nil, fmt.Errorf(
			"-allow-from을 쓰려면 -bind를 사설 주소로 두십시오. 지금은 %s여서 밖에서 닿지 않습니다", plan.bind)
	}

	// 이미 깔린 상자에 다시 돌리는 경우입니다.
	//
	// 비밀을 새로 만들면 PostgreSQL 쪽 비밀번호만 바뀌고 .env는 그대로
	// 남습니다. .env는 손으로 고친 값이 있을 수 있어 덮지 않기 때문입니다.
	// 그러면 설치는 성공했다고 나오고 중앙은 자기 데이터베이스에 붙지
	// 못합니다. 토큰도 새로 만들면 이미 붙어 있는 수집 노드가 전부
	// 떨어집니다. 있는 것을 그대로 씁니다.
	kept, err := existingSecrets(plan.prefix)
	if err != nil {
		return nil, err
	}
	if plan.token == "" {
		plan.token = kept.token
	}
	plan.dbPassword = kept.dbPassword

	if plan.token == "" {
		if plan.token, err = randomSecret(32); err != nil {
			return nil, err
		}
	}
	if plan.dbPassword == "" {
		if plan.dbPassword, err = randomSecret(24); err != nil {
			return nil, err
		}
	}
	return plan, nil
}

func isLoopbackHost(host string) bool {
	if host == "" || host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified()
	}
	return false
}

// minTokenLen은 서버 쪽 검사와 같아야 합니다. 여기서 느슨하게 받으면
// 설치는 되고 시작할 때 거부당합니다.
const minTokenLen = 16

// checkAccountName은 계정 이름이 셸과 systemd에 그대로 들어가도 되는지 봅니다.
func checkAccountName(name string) error {
	if name == "" {
		return errors.New("계정 이름이 비었습니다")
	}
	if name == "root" {
		return errors.New("root로 돌리지 마십시오. 서비스 전용 계정을 쓰십시오")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return fmt.Errorf("계정 이름에 쓸 수 없는 글자가 있습니다: %q", name)
		}
	}
	return nil
}

// sanitizeNodeID는 호스트 이름을 노드 아이디로 쓸 수 있게 다듬습니다.
func sanitizeNodeID(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i]
	}
	var b strings.Builder
	for _, r := range host {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "node"
	}
	return b.String()
}

// randomSecret은 토큰과 비밀번호를 만듭니다.
//
// 글자를 영문과 숫자로만 씁니다. SQL 문자열과 접속 주소 양쪽에 그대로
// 들어가는 값이라, 따옴표나 골뱅이가 섞이면 어느 한쪽이 깨집니다.
func randomSecret(n int) (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		pick, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", fmt.Errorf("무작위 값을 만들지 못했습니다: %w", err)
		}
		b[i] = alphabet[pick.Int64()]
	}
	return string(b), nil
}

func (p *installPlan) describe() {
	fmt.Printf("saucedust를 %s 역할로 깝니다.\n\n", p.role)
	fmt.Printf("  배포판 갈래   %s\n", p.family)
	fmt.Printf("  설치 폴더     %s\n", p.prefix)
	fmt.Printf("  서비스 계정   %s\n", p.account)
	fmt.Printf("  노드 아이디   %s\n", p.nodeID)
	if p.role == "control" {
		fmt.Printf("  열 주소       %s\n", p.bind)
		fmt.Printf("  축소본        %s\n", p.thumbDir)
	} else {
		fmt.Printf("  중앙 주소     %s\n", p.controlURL)
	}

	fmt.Println("\n할 일:")
	if pkgs := packagesFor(p.family, p.withServer); len(pkgs) > 0 {
		fmt.Printf("  꾸러미 설치   %s\n", strings.Join(pkgs, " "))
	}
	fmt.Printf("  계정 만들기   %s\n", p.account)
	if p.withServer {
		fmt.Println("  PostgreSQL    자료 폴더를 만들고 띄우고 sauce 계정과 데이터베이스를 만듭니다")
		if p.allowFrom != "" {
			fmt.Printf("                %s에서 붙게 엽니다\n", p.allowFrom)
		}
	}
	fmt.Println("  Python 워커   가상 환경을 만들고 torch를 깝니다")
	if !p.skipModels {
		fmt.Println("  모델 가중치   미리 받습니다. 몇 분 걸립니다")
	}
	if p.withServer {
		fmt.Println("  스키마        migrate를 돌리고 SigLIP 모델을 등록합니다")
	}
	fmt.Println("  systemd       유닛을 쓰고 켜고 띄웁니다")
	fmt.Println()
}

func confirm() bool {
	fmt.Print("진행할까요? [y/N] ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

func (p *installPlan) apply(ctx context.Context) error {
	steps := []struct {
		name string
		run  func(context.Context) error
	}{
		{"꾸러미를 깝니다", p.installPackages},
		{"서비스 계정을 만듭니다", p.ensureAccount},
		{"폴더를 만듭니다", p.ensureDirs},
		{"실행 파일을 놓습니다", p.copySelf},
	}
	if p.withServer {
		steps = append(steps, struct {
			name string
			run  func(context.Context) error
		}{"PostgreSQL을 준비합니다", p.setupPostgres})
	}
	steps = append(steps,
		struct {
			name string
			run  func(context.Context) error
		}{".env를 씁니다", p.writeEnv},
		struct {
			name string
			run  func(context.Context) error
		}{"Python 워커를 준비합니다", p.setupWorker},
	)
	if p.withServer {
		steps = append(steps, struct {
			name string
			run  func(context.Context) error
		}{"스키마와 모델을 등록합니다", p.migrateAndRegister})
	}
	steps = append(steps, struct {
		name string
		run  func(context.Context) error
	}{"systemd에 등록합니다", p.installUnits})

	for i, step := range steps {
		fmt.Printf("\n[%d/%d] %s\n", i+1, len(steps), step.name)
		if err := step.run(ctx); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}

	p.report()
	return nil
}

func (p *installPlan) installPackages(ctx context.Context) error {
	if refresh := refreshFor(p.family); len(refresh) > 0 {
		if err := runQuiet(ctx, refresh[0], refresh[1:]...); err != nil {
			return err
		}
	}
	pkgs := packagesFor(p.family, p.withServer)
	installer := installerFor(p.family)
	if len(pkgs) == 0 || len(installer) == 0 {
		return fmt.Errorf("%s 갈래의 꾸러미 이름을 모릅니다. 손으로 깔고 setup을 쓰십시오", p.family)
	}
	return runQuiet(ctx, installer[0], append(installer[1:], pkgs...)...)
}

// ensureAccount는 서비스 전용 계정을 만듭니다.
//
// root로 돌리면 안 됩니다. 이 프로그램은 바깥에서 받은 이미지를 열고
// 상대가 알려 준 주소로 붙습니다. 그 일을 root 권한으로 할 이유가 없습니다.
func (p *installPlan) ensureAccount(ctx context.Context) error {
	if _, err := user.Lookup(p.account); err == nil {
		fmt.Printf("  %s 계정이 이미 있습니다\n", p.account)
		return nil
	}
	return runQuiet(ctx, "useradd",
		"--system", "--home-dir", p.prefix, "--no-create-home",
		"--shell", "/sbin/nologin", p.account)
}

func (p *installPlan) ensureDirs(_ context.Context) error {
	uid, gid, err := lookupIDs(p.account)
	if err != nil {
		return err
	}
	for _, dir := range []string{p.prefix, p.dataDir, p.indexDir, p.thumbDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("%s를 만들지 못했습니다: %w", dir, err)
		}
		if err := os.Chown(dir, uid, gid); err != nil {
			return fmt.Errorf("%s의 임자를 바꾸지 못했습니다: %w", dir, err)
		}
	}
	return nil
}

// copySelf는 지금 도는 실행 파일을 설치 폴더에 놓습니다.
// 이미 그 자리에서 돌고 있으면 그대로 둡니다.
func (p *installPlan) copySelf(_ context.Context) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	target := filepath.Join(p.prefix, "saucedust")
	if same, _ := sameFile(self, target); same {
		fmt.Println("  이미 설치 폴더에 있습니다")
		return nil
	}

	raw, err := os.ReadFile(self) // #nosec G304 -- 지금 도는 실행 파일입니다
	if err != nil {
		return err
	}
	// 자리는 -prefix에서 오고 절대 경로인지 미리 봤습니다. root로 돌리는
	// 명령이라 자기 실행 파일을 자기가 정한 자리에 두는 것이 하는 일입니다.
	if err := os.WriteFile(target, raw, 0o755); err != nil { // #nosec G306,G703 -- 실행 파일이고 자리는 운영자가 정합니다
		return fmt.Errorf("%s에 놓지 못했습니다: %w", target, err)
	}
	uid, gid, err := lookupIDs(p.account)
	if err != nil {
		return err
	}
	return os.Chown(target, uid, gid)
}

func sameFile(a, b string) (bool, error) {
	fa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(fa, fb), nil
}

func lookupIDs(name string) (int, int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, fmt.Errorf("%s 계정을 찾지 못했습니다: %w", name, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

func runQuiet(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- 인자는 배포판 표에서 옵니다
	out, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(out))
		if len(trimmed) > 2000 {
			trimmed = trimmed[len(trimmed)-2000:]
		}
		return fmt.Errorf("%s가 실패했습니다: %w\n%s", name, err, trimmed)
	}
	return nil
}
