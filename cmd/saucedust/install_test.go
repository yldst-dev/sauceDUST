package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 역할을 빠뜨리거나 잘못 적으면 시작하지 않아야 합니다.
// 기본값으로 아무거나 골라 주면 엉뚱한 상자에 PostgreSQL이 깔립니다.
func TestInstallPlanRejectsBadRole(t *testing.T) {
	for _, role := range []string{"", "central", "Control", "both"} {
		if _, err := newInstallPlan(installOptions{role: role, prefix: "/opt/saucedust", account: "saucedust"}); err == nil {
			t.Errorf("역할 %q를 받아들였습니다", role)
		}
	}
}

// 수집 노드는 중앙 주소와 토큰이 없으면 설치할 의미가 없습니다.
// 없는 채로 깔면 서비스가 뜨자마자 401로 되돌아옵니다.
func TestWorkerPlanNeedsControlAndToken(t *testing.T) {
	long := strings.Repeat("a", minTokenLen)

	if _, err := newInstallPlan(installOptions{role: "worker", prefix: "/opt/saucedust", account: "saucedust", thumbDir: "", controlURL: "", token: long, bind: "", allowFrom: "", dbPassword: "pw"}); err == nil {
		t.Error("중앙 주소 없이 통과했습니다")
	}
	if _, err := newInstallPlan(installOptions{role: "worker", prefix: "/opt/saucedust", account: "saucedust", thumbDir: "", controlURL: "http://a:8000", token: "", bind: "", allowFrom: "", dbPassword: "pw"}); err == nil {
		t.Error("토큰 없이 통과했습니다")
	}
	short := strings.Repeat("a", minTokenLen-1)
	if _, err := newInstallPlan(installOptions{role: "worker", prefix: "/opt/saucedust", account: "saucedust", thumbDir: "", controlURL: "http://a:8000", token: short, bind: "", allowFrom: "", dbPassword: "pw"}); err == nil {
		t.Errorf("%d자 토큰을 받아들였습니다. 서버가 %d자 미만을 거부합니다", len(short), minTokenLen)
	}
	if _, err := newInstallPlan(installOptions{role: "worker", prefix: "/opt/saucedust", account: "saucedust", thumbDir: "", controlURL: "http://a:8000", token: long, bind: "", allowFrom: "", dbPassword: "pw"}); err != nil {
		t.Errorf("멀쩡한 값을 거절했습니다: %v", err)
	}
}

// 중앙 노드는 토큰과 비밀번호를 스스로 만들어야 합니다.
// 사람이 만들라고 하면 빈 값이나 짧은 값으로 깔립니다.
func TestControlPlanMakesItsOwnSecrets(t *testing.T) {
	a, err := newInstallPlan(installOptions{role: "control", prefix: "/opt/saucedust", account: "saucedust", thumbDir: "", controlURL: "", token: "", bind: "", allowFrom: "", dbPassword: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := newInstallPlan(installOptions{role: "control", prefix: "/opt/saucedust", account: "saucedust", thumbDir: "", controlURL: "", token: "", bind: "", allowFrom: "", dbPassword: "pw"})
	if err != nil {
		t.Fatal(err)
	}

	if len(a.token) < minTokenLen {
		t.Errorf("토큰이 %d자입니다. %d자 이상이어야 합니다", len(a.token), minTokenLen)
	}
	if a.token == b.token || a.dbPassword == b.dbPassword {
		t.Error("두 번 만든 비밀이 같습니다")
	}
	// 접속 주소와 SQL 문자열 양쪽에 그대로 들어갑니다.
	for _, secret := range []string{a.token, a.dbPassword} {
		if strings.ContainsAny(secret, `'"@:/\ `) {
			t.Errorf("비밀에 주소나 SQL을 깨뜨릴 글자가 있습니다: %q", secret)
		}
	}
}

func TestInstallPlanRejectsBadInput(t *testing.T) {
	tests := []struct {
		name                             string
		prefix, account, bind, allowFrom string
	}{
		{"상대 경로", "opt/saucedust", "saucedust", "", ""},
		{"root 계정", "/opt/saucedust", "root", "", ""},
		{"셸에 위험한 계정", "/opt/saucedust", "sauce;rm", "", ""},
		{"포트 없는 주소", "/opt/saucedust", "saucedust", "10.0.0.10", ""},
		{"대역이 아닌 값", "/opt/saucedust", "saucedust", "", "10.0.0.11"},
	}

	for _, tc := range tests {
		_, err := newInstallPlan(installOptions{role: "control", prefix: tc.prefix, account: tc.account, bind: tc.bind, allowFrom: tc.allowFrom})
		if err == nil {
			t.Errorf("%s를 받아들였습니다", tc.name)
		}
	}
}

func TestSanitizeNodeID(t *testing.T) {
	tests := []struct{ host, want string }{
		{"saucedust-central", "saucedust-central"},
		{"Central.example.com", "central"},
		{"NODE_1", "node1"},
		{"수집기", "node"},
		{"", "node"},
		{"   ", "node"},
	}
	for _, tc := range tests {
		if got := sanitizeNodeID(tc.host); got != tc.want {
			t.Errorf("%q에서 %q가 나왔습니다. %q여야 합니다", tc.host, got, tc.want)
		}
	}
}

// .env에는 역할에 맞는 값이 다 들어 있어야 합니다.
// 하나라도 비면 사용자가 손으로 채워야 하고, 그게 이 명령을 만든 이유입니다.
func TestEnvBodyHasEverything(t *testing.T) {
	control, err := newInstallPlan(installOptions{role: "control", prefix: "/opt/sd", account: "saucedust", thumbDir: "", controlURL: "", token: "", bind: "10.0.0.10:8000", allowFrom: "", dbPassword: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	body := control.envBody()
	for _, want := range []string{
		"SAUCEDUST_NODE_ROLE=control",
		"SAUCEDUST_INDEX=flat",
		"SAUCEDUST_INDEX_DIR=/opt/sd/index",
		"SAUCEDUST_CONTROL_BIND=10.0.0.10:8000",
		"SAUCEDUST_CONTROL_TOKEN=" + control.token,
		"postgres://sauce:" + control.dbPassword + "@localhost:5432/sauce",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("중앙 .env에 %q가 없습니다:\n%s", want, body)
		}
	}

	worker, err := newInstallPlan(installOptions{role: "worker", prefix: "/opt/sd", account: "saucedust", thumbDir: "", controlURL: "http://10.0.0.10:8000", token: strings.Repeat("t", 20), bind: "", allowFrom: "", dbPassword: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	body = worker.envBody()
	for _, want := range []string{
		"SAUCEDUST_NODE_ROLE=worker",
		"SAUCEDUST_CONTROL_URL=http://10.0.0.10:8000",
		// 2GB 상자에서 기본 16이면 내려받는 중인 것만 최악 512MB가 뜹니다.
		"CRAWL_MAX_CONCURRENCY=4",
		"@10.0.0.10:5432/sauce",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("수집 .env에 %q가 없습니다:\n%s", want, body)
		}
	}
	// 수집 노드가 중앙을 열어 주는 값을 가지면 안 됩니다.
	if strings.Contains(body, "SAUCEDUST_CONTROL_BIND") {
		t.Error("수집 .env에 여는 주소가 들어 있습니다")
	}
}

// 유닛 파일에 비밀이 들어가면 안 됩니다. 누구나 읽을 수 있는 파일입니다.
func TestUnitsCarryNoSecrets(t *testing.T) {
	plan, err := newInstallPlan(installOptions{role: "control", prefix: "/opt/sd", account: "saucedust", thumbDir: "", controlURL: "", token: "", bind: "", allowFrom: "", dbPassword: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	for _, unit := range plan.units() {
		if strings.Contains(unit.body, plan.token) {
			t.Errorf("%s에 토큰이 있습니다", unit.name)
		}
		if strings.Contains(unit.body, plan.dbPassword) {
			t.Errorf("%s에 비밀번호가 있습니다", unit.name)
		}
		if strings.Contains(unit.body, "User=root") {
			t.Errorf("%s가 root로 돕니다", unit.name)
		}
		// ProtectSystem=strict를 걸었으면 쓸 곳을 다시 열어야 합니다.
		// 빠뜨리면 서비스가 뜨자마자 쓰기 오류로 죽습니다.
		if strings.Contains(unit.body, "ProtectSystem=strict") &&
			!strings.Contains(unit.body, "ReadWritePaths=") {
			t.Errorf("%s가 온 시스템을 읽기 전용으로 두고 쓸 곳을 열지 않았습니다", unit.name)
		}
	}
}

// 두 역할에 필요한 유닛이 다릅니다. 중앙에 crawl을 얹으면 4GB에서 몫이
// 모자라고, 수집 노드에 control을 얹으면 PostgreSQL을 찾다 죽습니다.
func TestUnitsMatchTheRole(t *testing.T) {
	control, err := newInstallPlan(installOptions{role: "control", prefix: "/opt/sd", account: "saucedust", thumbDir: "", controlURL: "", token: "", bind: "", allowFrom: "", dbPassword: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := newInstallPlan(installOptions{role: "worker", prefix: "/opt/sd", account: "saucedust", thumbDir: "", controlURL: "http://a:8000", token: strings.Repeat("t", 20), bind: "", allowFrom: "", dbPassword: "pw"})
	if err != nil {
		t.Fatal(err)
	}

	names := func(p *installPlan) string {
		var b strings.Builder
		for _, u := range p.units() {
			b.WriteString(u.name + " ")
		}
		return b.String()
	}

	got := names(control)
	if !strings.Contains(got, "saucedust-control.service") ||
		!strings.Contains(got, "saucedust-embed.service") {
		t.Errorf("중앙 유닛이 %q입니다", got)
	}
	if strings.Contains(got, "saucedust-crawl.service") {
		t.Errorf("중앙에 수집 유닛이 붙었습니다: %q", got)
	}

	got = names(worker)
	if !strings.Contains(got, "saucedust-crawl.service") ||
		!strings.Contains(got, "saucedust-embed.service") {
		t.Errorf("수집 유닛이 %q입니다", got)
	}
	if strings.Contains(got, "saucedust-control.service") {
		t.Errorf("수집 노드에 중앙 유닛이 붙었습니다: %q", got)
	}

	// 중앙만 PostgreSQL 서버가 필요합니다.
	if !control.withServer {
		t.Error("중앙이 PostgreSQL 서버를 안 깝니다")
	}
	if worker.withServer {
		t.Error("수집 노드가 쓰지도 않는 PostgreSQL 서버를 깝니다")
	}
}

// 배포판마다 꾸러미와 명령이 달라야 하고, 모르는 갈래는 빈 값이어야 합니다.
func TestPackagesAndInstallerByFamily(t *testing.T) {
	for _, family := range []string{"rhel", "debian", "suse", "arch"} {
		if len(packagesFor(family, false)) == 0 {
			t.Errorf("%s의 꾸러미 목록이 비었습니다", family)
		}
		if len(installerFor(family)) == 0 {
			t.Errorf("%s의 설치 명령이 비었습니다", family)
		}
		// 수집 노드에 서버를 깔면 안 씁니다.
		client := packagesFor(family, false)
		server := packagesFor(family, true)
		if len(server) < len(client) {
			t.Errorf("%s에서 서버까지 깔 목록이 더 짧습니다", family)
		}
	}
	if len(packagesFor("plan9", true)) != 0 || len(installerFor("plan9")) != 0 {
		t.Error("모르는 갈래에 꾸러미나 명령을 냈습니다")
	}
}

// 이미 깔린 상자에 다시 돌리면 비밀을 이어 써야 합니다.
//
// 새로 만들면 PostgreSQL 쪽 비밀번호만 바뀌고 .env는 그대로 남습니다.
// .env는 손으로 고친 값이 있을 수 있어 덮지 않기 때문입니다. 그러면
// 설치는 성공했다고 나오고 중앙은 자기 데이터베이스에 붙지 못합니다.
func TestRerunKeepsSecrets(t *testing.T) {
	dir := t.TempDir()

	first, err := newInstallPlan(installOptions{role: "control", prefix: dir, account: "saucedust", thumbDir: "", controlURL: "", token: "", bind: "10.0.0.10:8000", allowFrom: "", dbPassword: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"),
		[]byte(first.envBody()), 0o600); err != nil {
		t.Fatal(err)
	}

	second, err := newInstallPlan(installOptions{role: "control", prefix: dir, account: "saucedust", thumbDir: "", controlURL: "", token: "", bind: "10.0.0.10:8000", allowFrom: "", dbPassword: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if second.token != first.token {
		t.Errorf("토큰이 바뀌었습니다. 붙어 있는 수집 노드가 전부 떨어집니다")
	}
	if second.dbPassword != first.dbPassword {
		t.Errorf("비밀번호가 바뀌었습니다. .env는 안 고쳐지므로 중앙이 못 붙습니다")
	}
}

// 첫 설치에서는 이어 쓸 것이 없어야 합니다.
func TestFirstInstallHasNothingToKeep(t *testing.T) {
	kept, err := existingSecrets(t.TempDir())
	if err != nil {
		t.Fatalf("없는 .env를 오류로 봤습니다: %v", err)
	}
	if kept.token != "" || kept.dbPassword != "" {
		t.Errorf("빈 폴더에서 값이 나왔습니다: %+v", kept)
	}
}

// 되돌아오는 주소에 붙여 두고 다른 대역을 열면 수집 노드가 못 붙습니다.
// 설치는 성공하고 왜 안 되는지는 안 알려 주는 형태라 미리 막습니다.
func TestAllowFromNeedsAReachableBind(t *testing.T) {
	for _, bind := range []string{"127.0.0.1:8000", "localhost:8000", "[::1]:8000", "0.0.0.0:8000"} {
		_, err := newInstallPlan(installOptions{role: "control", prefix: t.TempDir(), account: "saucedust", thumbDir: "", controlURL: "", token: "", bind: bind, allowFrom: "10.0.0.0/24", dbPassword: "pw"})
		if err == nil {
			t.Errorf("bind가 %s인데 -allow-from을 받아들였습니다", bind)
		}
	}
	if _, err := newInstallPlan(installOptions{role: "control", prefix: t.TempDir(), account: "saucedust", bind: "10.0.0.10:8000", allowFrom: "10.0.0.0/24"}); err != nil {
		t.Errorf("사설 주소에 붙이는 멀쩡한 조합을 거절했습니다: %v", err)
	}
}

func TestParseEnvLines(t *testing.T) {
	body := "# 설명\n\nA=1\n  B = 2  \nC\nD=a=b\n"
	got := parseEnvLines(body)
	want := map[string]string{"A": "1", "B": "2", "D": "a=b"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s가 %q입니다. %q여야 합니다", k, got[k], v)
		}
	}
	if _, ok := got["C"]; ok {
		t.Error("등호가 없는 줄을 넣었습니다")
	}
	if len(got) != len(want) {
		t.Errorf("항목이 %d개입니다. %d개여야 합니다: %v", len(got), len(want), got)
	}
}

// 값 셋을 따로 옮겨 적게 하면 하나를 빠뜨립니다. 한 덩어리로 오갑니다.
func TestJoinTicketRoundTrip(t *testing.T) {
	control, err := newInstallPlan(installOptions{
		role: "control", prefix: t.TempDir(), account: "saucedust", bind: "10.0.0.10:8000",
	})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := control.joinBlob()
	if err != nil {
		t.Fatal(err)
	}

	worker, err := newInstallPlan(installOptions{
		role: "worker", prefix: t.TempDir(), account: "saucedust", join: blob,
	})
	if err != nil {
		t.Fatalf("중앙이 낸 값을 수집 노드가 못 읽었습니다: %v", err)
	}
	if worker.token != control.token {
		t.Error("토큰이 옮겨지지 않았습니다")
	}
	if worker.dbPassword != control.dbPassword {
		t.Error("비밀번호가 옮겨지지 않았습니다")
	}
	if worker.controlURL != "http://10.0.0.10:8000" {
		t.Errorf("중앙 주소가 %q입니다", worker.controlURL)
	}

	// 그 값이 그대로 .env에 들어가야 붙습니다.
	if !strings.Contains(worker.envBody(),
		"postgres://sauce:"+control.dbPassword+"@10.0.0.10:5432/sauce") {
		t.Errorf("수집 .env의 접속 주소에 비밀번호가 없습니다:\n%s", worker.envBody())
	}
}

// 깨진 값을 받으면 꾸러미를 다 깔기 전에 멈춰야 합니다.
func TestJoinTicketRejectsGarbage(t *testing.T) {
	full, err := joinTicket{ControlURL: "http://a:8000", Token: "t", DBPassword: "p"}.encode()
	if err != nil {
		t.Fatal(err)
	}
	partial, err := joinTicket{ControlURL: "http://a:8000"}.encode()
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, blob string }{
		{"base64가 아님", "이건 값이 아닙니다"},
		{"JSON이 아님", "bm90IGpzb24"},
		{"값이 빠짐", partial},
		{"뒤가 잘림", full[:len(full)-6]},
	} {
		if _, err := decodeJoinTicket(tc.blob); err == nil {
			t.Errorf("%s를 받아들였습니다", tc.name)
		}
	}

	// 앞뒤 공백은 붙여 넣다 흔히 붙습니다. 그것 때문에 막지는 않습니다.
	if _, err := decodeJoinTicket("  " + full + "\n"); err != nil {
		t.Errorf("공백이 붙은 멀쩡한 값을 거절했습니다: %v", err)
	}
}

// 수집 노드는 비밀번호 없이 깔리면 안 됩니다.
// 예전에는 .env에 "비밀번호"라는 글자가 들어가고 설치는 성공했습니다.
func TestWorkerNeedsDBPassword(t *testing.T) {
	_, err := newInstallPlan(installOptions{
		role: "worker", prefix: t.TempDir(), account: "saucedust",
		controlURL: "http://a:8000", token: strings.Repeat("t", 20),
	})
	if err == nil {
		t.Error("비밀번호 없이 통과했습니다")
	}
}
