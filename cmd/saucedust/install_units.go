package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const unitDir = "/etc/systemd/system"

// unitFile은 쓸 유닛 하나입니다.
type unitFile struct {
	name string
	body string
}

// units는 이 역할에 필요한 유닛들을 냅니다.
//
// 임베딩 워커는 두 역할 다 필요합니다. 중앙도 질의 이미지를 벡터로
// 바꿔야 하기 때문입니다. 그 위에 중앙은 control을, 수집 노드는
// worker를 얹습니다.
func (p *installPlan) units() []unitFile {
	worker := unitFile{
		name: "saucedust-embed.service",
		body: p.unitBody(unitSpec{
			description: "saucedust embedding worker",
			workDir:     filepath.Join(p.prefix, "worker"),
			exec: fmt.Sprintf("%s main:app --host 127.0.0.1 --port 8100",
				filepath.Join(p.prefix, "worker", ".venv", "bin", "uvicorn")),
			after: "network-online.target",
		}),
	}

	if p.role == "control" {
		return []unitFile{worker, {
			name: "saucedust-control.service",
			body: p.unitBody(unitSpec{
				description: "saucedust control node",
				workDir:     p.prefix,
				// 수집은 수집 노드에 맡깁니다. 중앙에서 함께 돌리면
				// 워커가 두 배로 붙어 4GB 상자에서는 몫이 모자랍니다.
				exec:     filepath.Join(p.prefix, "saucedust") + " control -no-crawl",
				after:    "network-online.target postgresql.service saucedust-embed.service",
				requires: "postgresql.service saucedust-embed.service",
			}),
		}}
	}

	return []unitFile{worker, {
		name: "saucedust-crawl.service",
		body: p.unitBody(unitSpec{
			description: "saucedust crawl node",
			workDir:     p.prefix,
			exec:        filepath.Join(p.prefix, "saucedust") + " worker",
			after:       "network-online.target saucedust-embed.service",
			requires:    "saucedust-embed.service",
		}),
	}}
}

type unitSpec struct {
	description string
	workDir     string
	exec        string
	after       string
	requires    string
}

// unitBody는 유닛 내용을 만듭니다.
//
// 비밀은 여기 넣지 않습니다. 유닛 파일은 누구나 읽을 수 있어야 하고,
// 토큰과 비밀번호는 설치 폴더의 .env에만 둡니다. 프로그램이 작업
// 폴더에서 .env를 읽으므로 WorkingDirectory만 맞추면 됩니다.
func (p *installPlan) unitBody(spec unitSpec) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	fmt.Fprintf(&b, "Description=%s\n", spec.description)
	fmt.Fprintf(&b, "After=%s\n", spec.after)
	if spec.requires != "" {
		fmt.Fprintf(&b, "Requires=%s\n", spec.requires)
	}

	b.WriteString("\n[Service]\n")
	fmt.Fprintf(&b, "User=%s\n", p.account)
	fmt.Fprintf(&b, "WorkingDirectory=%s\n", spec.workDir)
	fmt.Fprintf(&b, "Environment=HOME=%s\n", p.prefix)
	fmt.Fprintf(&b, "Environment=XDG_CACHE_HOME=%s\n", filepath.Join(p.prefix, ".cache"))
	fmt.Fprintf(&b, "ExecStart=%s\n", spec.exec)
	b.WriteString("Restart=always\nRestartSec=5\n")

	// 이 프로그램은 바깥에서 받은 이미지를 열고 상대가 알려 준 주소로
	// 붙습니다. 그런 일을 하는 것에는 필요 없는 권한을 미리 빼 둡니다.
	b.WriteString("NoNewPrivileges=yes\n")
	b.WriteString("PrivateTmp=yes\n")
	b.WriteString("ProtectSystem=strict\n")
	b.WriteString("ProtectHome=yes\n")
	b.WriteString("ProtectKernelTunables=yes\n")
	b.WriteString("ProtectControlGroups=yes\n")
	b.WriteString("RestrictSUIDSGID=yes\n")
	// ProtectSystem=strict가 온 시스템을 읽기 전용으로 두므로,
	// 우리가 쓰는 곳만 다시 열어 줍니다.
	paths := []string{p.prefix}
	if p.thumbDir != "" && !strings.HasPrefix(p.thumbDir, p.prefix+string(os.PathSeparator)) {
		paths = append(paths, p.thumbDir)
	}
	fmt.Fprintf(&b, "ReadWritePaths=%s\n", strings.Join(paths, " "))

	b.WriteString("\n[Install]\nWantedBy=multi-user.target\n")
	return b.String()
}

// writeJoinFile은 수집 노드용 값을 파일로도 남깁니다.
//
// 보고에 찍어 주기만 하면 사람이 눈으로 옮겨야 합니다. 자동으로 두 대를
// 세우는 대본은 그것을 읽을 수 없습니다. 파일로 두면 qm guest exec나
// ssh로 그대로 꺼내 갑니다.
//
// 비밀이 들어 있으므로 본인만 읽게 둡니다.
func (p *installPlan) writeJoinFile() error {
	if p.role != "control" {
		return nil
	}
	blob, err := p.joinBlob()
	if err != nil {
		return err
	}

	target := filepath.Join(p.prefix, "join.txt")
	if err := os.WriteFile(target, []byte(blob+"\n"), 0o600); err != nil {
		return fmt.Errorf("%s를 쓰지 못했습니다: %w", target, err)
	}
	uid, gid, err := lookupIDs(p.account)
	if err != nil {
		return err
	}
	return os.Chown(target, uid, gid)
}

// installUnits는 유닛을 쓰고 켜고 띄웁니다.
func (p *installPlan) installUnits(ctx context.Context) error {
	list := p.units()
	names := make([]string, 0, len(list))
	for _, unit := range list {
		target := filepath.Join(unitDir, unit.name)
		// 유닛은 계획에서 그대로 만들어지므로 덮어써도 잃을 것이 없습니다.
		if err := os.WriteFile(target, []byte(unit.body), 0o644); err != nil { // #nosec G306 -- systemd가 읽어야 합니다
			return fmt.Errorf("%s를 쓰지 못했습니다: %w", target, err)
		}
		names = append(names, unit.name)
	}

	if err := runQuiet(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	return runQuiet(ctx, "systemctl", append([]string{"enable", "--now"}, names...)...)
}

// report는 끝난 뒤 무엇을 확인하고 무엇을 옮겨 적어야 하는지 알려 줍니다.
func (p *installPlan) report() {
	fmt.Println("\n끝났습니다.")

	list := p.units()
	names := make([]string, 0, len(list))
	for _, unit := range list {
		names = append(names, strings.TrimSuffix(unit.name, ".service"))
	}
	fmt.Printf("\n  상태 보기   systemctl status %s\n", strings.Join(names, " "))
	fmt.Printf("  기록 보기   journalctl -u %s -f\n", names[len(names)-1])
	fmt.Printf("  점검        %s/saucedust doctor\n", p.prefix)

	if p.role == "worker" {
		fmt.Println("\n채울 것은 없습니다. 중앙에 붙었는지는 중앙에서 node ls로 보십시오.")
		return
	}

	fmt.Printf("\n대시보드   http://%s\n", p.bind)

	blob, err := p.joinBlob()
	if err != nil {
		fmt.Printf("\n수집 노드용 값을 만들지 못했습니다: %v\n", err)
		return
	}
	fmt.Println("\n수집 노드는 그 상자에서 이 한 줄만 돌리면 됩니다.")
	fmt.Printf("\n  sudo ./saucedust install -role worker -join %s -yes\n", blob)

	if p.allowFrom == "" {
		fmt.Println("\n다만 지금은 이 상자의 PostgreSQL이 밖에서 안 보입니다.")
		fmt.Println("수집 노드를 다른 상자에 둘 것이면 먼저 여기서 이렇게 여십시오.")
		fmt.Printf("  sudo ./saucedust install -role control -bind 사설주소:8000 -allow-from 수집노드대역 -yes\n")
	}
	fmt.Printf("\n위 값에는 토큰과 비밀번호가 들어 있습니다. 잃으면 %s/.env에서 다시 만듭니다.\n", p.prefix)
}
