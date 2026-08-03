//go:build windows

package main

import (
	"context"
	"errors"
)

// runAs는 Windows에서는 쓰지 않습니다.
//
// install은 리눅스 전용입니다. cmdInstall이 먼저 막지만, 빌드가 되게
// 하려면 이 자리가 있어야 합니다. 조용히 root로 돌리는 대신 막습니다.
func (p *installPlan) runAs(context.Context, string, string, ...string) error {
	return errors.New("install은 리눅스에서만 씁니다. Windows에서는 setup을 쓰십시오")
}
