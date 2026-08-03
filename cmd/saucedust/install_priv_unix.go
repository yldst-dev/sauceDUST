//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// runAs는 서비스 계정으로 명령을 돌립니다.
//
// pip와 가중치 내려받기를 root로 하면 그 결과물이 root 것이 되어,
// 서비스 계정이 읽지 못하거나 캐시를 못 씁니다. 처음 요청에서야
// 드러나는 형태라 여기서 계정을 맞춥니다.
func (p *installPlan) runAs(ctx context.Context, dir, name string, args ...string) error {
	uid, gid, err := lookupIDs(p.account)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- 경로는 설치 계획에서 옵니다
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}, // #nosec G115 -- 계정 조회에서 온 값입니다
	}
	// HOME이 없으면 pip와 huggingface가 root의 캐시를 보려 합니다.
	cmd.Env = append(os.Environ(), "HOME="+p.prefix, "XDG_CACHE_HOME="+filepath.Join(p.prefix, ".cache"))
	return cmd.Run()
}
