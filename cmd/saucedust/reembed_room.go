package main

import (
	"context"
	"fmt"

	"saucedust/internal/app"
	"saucedust/internal/domain"
)

// checkReembedRoom은 다시 계산해 넣을 자리가 있는지 봅니다.
//
// 상한은 담긴 '장수'만 봅니다. 그런데 실제로 필요한 메모리는 장수와
// 모델 수를 곱한 값입니다. reembed는 장수를 하나도 늘리지 않으면서
// 모델을 더하므로, 적재 경계의 검사도 수집 쪽 검사도 전부 지나갑니다.
//
// 768차원 하나는 장당 1,046바이트이고 512차원을 더하면 1,759바이트로
// 1.68배가 됩니다. 꽉 찬 색인에 모델을 더하면 그만큼이 그대로 모자라고,
// Qdrant는 느려지는 것이 아니라 죽습니다. 다시 띄워도 컬렉션을 여는
// 것 자체가 한도를 넘어 또 죽어서 지우는 것 말고는 방법이 없습니다.
func checkReembedRoom(ctx context.Context, rt *nodeRuntime, targets []domain.EmbeddingModel) error {
	if rt.cfg.MaxIndexed <= 0 {
		return nil
	}
	// 납작한 색인은 장수에 비례해 램을 붙들지 않습니다. 다시 계산해도
	// 메모리 때문에 죽을 일이 없으므로 여기서 막을 이유가 없습니다.
	if rt.cfg.IndexKind != domain.IndexQdrant {
		return nil
	}

	all, err := rt.store.AllModels(ctx)
	if err != nil {
		return fmt.Errorf("등록된 모델을 읽지 못했습니다: %w", err)
	}
	count, err := rt.store.CountImages(ctx)
	if err != nil {
		return fmt.Errorf("담긴 장수를 세지 못했습니다: %w", err)
	}

	sizes := make([]int, 0, len(all))
	for _, m := range all {
		sizes = append(sizes, m.VectorSize)
	}
	perImage := app.IndexBytesPerImage(domain.IndexQdrant, sizes)
	if perImage <= 0 {
		return nil
	}

	// 상한을 정할 때 잡았던 예산입니다. 지금 모델 구성으로 그 예산 안에
	// 들어가는지 봅니다.
	budget := rt.cfg.MaxIndexed * perImage
	need := count * perImage
	if need <= budget {
		return nil
	}

	fits := budget / perImage
	return fmt.Errorf(
		"지금 모델 %d개 구성에서는 %s장까지만 담깁니다. 이미 %s장이 있어 다시 계산하면 넘칩니다."+
			" 메모리를 늘리고 SAUCEDUST_MAX_INDEXED를 올리거나, 모델을 줄이십시오",
		len(all), comma(fits), comma(count))
}
