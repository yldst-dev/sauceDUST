package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// setupPostgres는 PostgreSQL을 쓸 수 있는 상태로 만듭니다.
//
// 자료 폴더 만들기, 띄우기, 계정과 데이터베이스 만들기, 그리고 필요하면
// 다른 노드가 붙게 여는 것까지입니다. 손으로 하면 여기가 제일 많이
// 틀리는 곳입니다. RHEL 갈래는 initdb를 안 해 주는데 그것을 빠뜨리면
// systemctl start가 이유도 없이 실패합니다.
func (p *installPlan) setupPostgres(ctx context.Context) error {
	if err := p.initPostgresData(ctx); err != nil {
		return err
	}
	if err := runQuiet(ctx, "systemctl", "enable", "--now", "postgresql"); err != nil {
		return err
	}
	if err := waitForPostgres(ctx); err != nil {
		return err
	}
	if err := p.ensureRoleAndDB(ctx); err != nil {
		return err
	}
	if p.allowFrom == "" {
		return nil
	}
	return p.openPostgresTo(ctx)
}

// initPostgresData는 자료 폴더가 없으면 만듭니다.
// Debian은 꾸러미가 알아서 하므로 할 일이 없습니다.
func (p *installPlan) initPostgresData(ctx context.Context) error {
	init := initdbFor(p.family)
	if len(init) == 0 {
		return nil
	}
	// 이미 만들어져 있으면 다시 하면 안 됩니다. initdb는 빈 폴더만
	// 받지만, 갈래마다 자리가 달라 여기서는 시작해 보고 판단합니다.
	if err := runQuiet(ctx, "systemctl", "start", "postgresql"); err == nil {
		fmt.Println("  PostgreSQL이 이미 준비되어 있습니다")
		return nil
	}
	if init[0] == "initdb" {
		return runQuiet(ctx, "su", "-", "postgres", "-c", "initdb -D /var/lib/pgsql/data")
	}
	return runQuiet(ctx, init[0], init[1:]...)
}

func waitForPostgres(ctx context.Context) error {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if err := runQuiet(ctx, "su", "-", "postgres", "-c", "psql -tAc 'select 1'"); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("PostgreSQL이 60초 안에 뜨지 않았습니다. systemctl status postgresql을 보십시오")
}

// psqlAsPostgres는 postgres 계정으로 SQL을 돌립니다.
//
// 비밀번호가 인자로 들어가므로 명령줄에 두지 않습니다. ps로 다른
// 사용자에게 보입니다. 표준 입력으로 넘깁니다.
func psqlAsPostgres(ctx context.Context, sql string) (string, error) {
	cmd := exec.CommandContext(ctx, "su", "-", "postgres", "-c", "psql -tAX -v ON_ERROR_STOP=1 -f -")
	cmd.Stdin = strings.NewReader(sql)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("psql이 실패했습니다: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ensureRoleAndDB는 sauce 계정과 데이터베이스를 만듭니다.
//
// 이미 있으면 비밀번호만 우리가 만든 것으로 맞춥니다. 그러지 않으면
// 예전 설치에서 남은 비밀번호를 모르는 채로 .env를 쓰게 되어, 붙지
// 못하는 설정이 완성됩니다.
func (p *installPlan) ensureRoleAndDB(ctx context.Context) error {
	exists, err := psqlAsPostgres(ctx, "select 1 from pg_roles where rolname = 'sauce';")
	if err != nil {
		return err
	}

	verb := "create role"
	if strings.TrimSpace(exists) == "1" {
		verb = "alter role"
		fmt.Println("  sauce 계정이 이미 있어 비밀번호를 다시 맞춥니다")
	}
	// 비밀번호는 영문과 숫자만이라 문자열 안에 그대로 두어도 됩니다.
	if _, err := psqlAsPostgres(ctx,
		fmt.Sprintf("%s sauce login password '%s';", verb, p.dbPassword)); err != nil {
		return err
	}

	dbExists, err := psqlAsPostgres(ctx, "select 1 from pg_database where datname = 'sauce';")
	if err != nil {
		return err
	}
	if strings.TrimSpace(dbExists) == "1" {
		fmt.Println("  sauce 데이터베이스가 이미 있습니다")
		return nil
	}
	_, err = psqlAsPostgres(ctx, "create database sauce owner sauce;")
	return err
}

// openPostgresTo는 그 대역에서 sauce 데이터베이스에 붙게 엽니다.
//
// 수집 노드가 구간을 빌리려면 PostgreSQL에 직접 붙습니다. 여는 범위를
// 좁힙니다. listen_addresses를 별표로 두고 pg_hba를 0.0.0.0/0으로 열면
// 그 순간부터 인터넷 전체가 비밀번호만 맞히면 됩니다.
func (p *installPlan) openPostgresTo(ctx context.Context) error {
	hba, err := psqlAsPostgres(ctx, "show hba_file;")
	if err != nil {
		return err
	}
	conf, err := psqlAsPostgres(ctx, "show config_file;")
	if err != nil {
		return err
	}

	// 어느 주소로 붙을지는 -bind에 적힌 것을 씁니다. 별표로 열지 않습니다.
	host, _, err := splitHostPortLoose(p.bind)
	if err != nil {
		return err
	}
	listen := "localhost"
	if host != "" && host != "127.0.0.1" && host != "::1" && host != "localhost" {
		listen = "localhost," + host
	}

	if err := appendOnce(conf, fmt.Sprintf("listen_addresses = '%s'", listen),
		"# saucedust install"); err != nil {
		return err
	}
	line := fmt.Sprintf("host  sauce  sauce  %s  scram-sha-256", p.allowFrom)
	if err := appendOnce(hba, line, "# saucedust install"); err != nil {
		return err
	}
	return runQuiet(ctx, "systemctl", "reload", "postgresql")
}

// appendOnce는 그 줄이 아직 없으면 덧붙입니다.
// 설치를 두 번 돌려도 같은 줄이 쌓이지 않아야 합니다.
func appendOnce(path, line, marker string) error {
	raw, err := os.ReadFile(path) // #nosec G304 -- PostgreSQL이 알려 준 자기 설정 파일입니다
	if err != nil {
		return fmt.Errorf("%s를 읽지 못했습니다: %w", path, err)
	}
	if strings.Contains(string(raw), line) {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- 위와 같습니다
	if err != nil {
		return fmt.Errorf("%s를 열지 못했습니다: %w", path, err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "\n%s\n%s\n", marker, line); err != nil {
		return err
	}
	return f.Close()
}

func splitHostPortLoose(addr string) (string, string, error) {
	i := strings.LastIndexByte(addr, ':')
	if i < 0 {
		return "", "", fmt.Errorf("주소에 포트가 없습니다: %s", addr)
	}
	return strings.Trim(addr[:i], "[]"), addr[i+1:], nil
}

// keptSecrets는 이미 깔린 설치에서 이어 쓸 값입니다.
type keptSecrets struct {
	token      string
	dbPassword string
}

// existingSecrets는 설치 폴더의 .env에서 토큰과 비밀번호를 꺼냅니다.
// 없으면 빈 값을 냅니다. 없는 것은 오류가 아닙니다. 첫 설치입니다.
func existingSecrets(prefix string) (keptSecrets, error) {
	var kept keptSecrets

	path := filepath.Join(prefix, ".env")
	raw, err := os.ReadFile(path) // #nosec G304 -- 설치 폴더 기준 고정 이름입니다
	if err != nil {
		if os.IsNotExist(err) {
			return kept, nil
		}
		return kept, fmt.Errorf("%s를 읽지 못했습니다: %w", path, err)
	}

	values := parseEnvLines(string(raw))
	kept.token = values["SAUCEDUST_CONTROL_TOKEN"]
	if dsn := values["DATABASE_URL"]; dsn != "" {
		if parsed, err := url.Parse(dsn); err == nil {
			if pw, ok := parsed.User.Password(); ok {
				kept.dbPassword = pw
			}
		}
	}
	return kept, nil
}

// parseEnvLines는 KEY=VALUE 줄을 읽습니다.
// 설정을 읽는 것이 아니라 우리가 쓴 값을 되찾는 용도라 단순하게 둡니다.
func parseEnvLines(body string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out
}

// writeEnv는 설정 파일을 채워 씁니다.
//
// 예제를 그대로 복사하고 사람에게 채우라고 하면, 어느 값이 반드시
// 필요하고 어느 값이 그대로 두면 되는지 알 수 없습니다. 여기서는
// 정해진 것을 다 넣고, 남는 것은 예제 기본값을 그대로 씁니다.
func (p *installPlan) writeEnv(_ context.Context) error {
	target := filepath.Join(p.prefix, ".env")
	if _, err := os.Stat(target); err == nil {
		// 이미 있는 것을 덮으면 손으로 고쳐 둔 값과 토큰이 사라집니다.
		// 되돌릴 수 없으므로 건드리지 않습니다.
		fmt.Printf("  %s가 이미 있어 그대로 둡니다\n", target)
		return nil
	}

	body := p.envBody()
	if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
		return fmt.Errorf("%s를 쓰지 못했습니다: %w", target, err)
	}
	uid, gid, err := lookupIDs(p.account)
	if err != nil {
		return err
	}
	return os.Chown(target, uid, gid)
}

// envBody는 .env 내용을 만듭니다. 시험에서 값을 확인할 수 있게 나눠 뒀습니다.
func (p *installPlan) envBody() string {
	var b strings.Builder
	b.WriteString("# saucedust install이 만들었습니다.\n")
	b.WriteString("# 토큰과 비밀번호가 들어 있으므로 본인만 읽을 수 있게 두십시오.\n\n")

	set := func(key, value string) {
		fmt.Fprintf(&b, "%s=%s\n", key, value)
	}

	set("SAUCEDUST_NODE_ID", p.nodeID)
	set("SAUCEDUST_NODE_ROLE", p.role)
	set("SAUCEDUST_DATA_DIR", p.dataDir)
	set("SAUCEDUST_INDEX", "flat")
	set("SAUCEDUST_INDEX_DIR", p.indexDir)
	b.WriteString("\n")

	if p.role == "control" {
		set("DATABASE_URL", fmt.Sprintf("postgres://sauce:%s@localhost:5432/sauce", p.dbPassword))
		set("SAUCEDUST_THUMB_DIR", p.thumbDir)
		set("SAUCEDUST_CONTROL_BIND", p.bind)
	} else {
		host, _, _ := splitHostPortLoose(strings.TrimPrefix(
			strings.TrimPrefix(p.controlURL, "http://"), "https://"))
		if host == "" {
			host = "중앙주소"
		}
		set("DATABASE_URL", fmt.Sprintf("postgres://sauce:%s@%s:5432/sauce", p.dbPassword, host))
		set("SAUCEDUST_CONTROL_URL", strings.TrimRight(p.controlURL, "/"))
		// 2GB 상자에서 기본 16이면 내려받는 중인 것만 최악 512MB가 뜹니다.
		// 상대 제한이 초당 5장이라 낮춰도 느려지지 않습니다.
		set("CRAWL_MAX_CONCURRENCY", "4")
	}
	set("SAUCEDUST_CONTROL_TOKEN", p.token)
	return b.String()
}

// setupWorker는 Python 쪽을 준비합니다. setup이 하던 일을 그대로 씁니다.
func (p *installPlan) setupWorker(ctx context.Context) error {
	self := filepath.Join(p.prefix, "saucedust")
	args := []string{"setup"}
	if p.skipModels {
		args = append(args, "-skip-models")
	}
	return p.runAs(ctx, p.prefix, self, args...)
}

// migrateAndRegister는 스키마를 올리고 모델을 등록합니다.
//
// 모델 등록을 빠뜨리면 수집 노드가 자기 워커의 모델을 기준과 대조하다
// 어긋난다고 보고 일을 아예 받지 않습니다. 설치에서 함께 합니다.
func (p *installPlan) migrateAndRegister(ctx context.Context) error {
	self := filepath.Join(p.prefix, "saucedust")
	if err := p.runAs(ctx, p.prefix, self, "migrate"); err != nil {
		return err
	}
	// 이미 등록돼 있으면 model add가 거절합니다. 목록을 먼저 봅니다.
	if err := p.runAs(ctx, p.prefix, self, "model", "ls"); err == nil {
		fmt.Println("  등록된 모델을 확인했습니다")
	}
	return p.runAs(ctx, p.prefix, self, "model", "sync")
}
