package main

import (
	"context"
	"log/slog"
	"strconv"

	"saucedust/internal/app"
	"saucedust/internal/domain"
)

// 담을 수 있는 장수를 알려 줍니다.
//
// 상한을 두지 않으면 어느 순간 Qdrant가 OOM으로 죽습니다. 서서히 느려지다
// 알려 주는 것이 아니라 한 번에 죽고, 다시 띄워도 컬렉션을 여는 것 자체가
// 한도를 넘어 또 죽습니다. 지우는 것 말고 되살릴 방법이 없습니다.
//
// 그래서 사용자가 계산해서 넣기를 기다리지 않고, 이 컴퓨터 메모리로 몇 장이
// 들어가는지 시작할 때 알려 줍니다.
func reportCapacity(ctx context.Context, rt *nodeRuntime, active []domain.EmbeddingModel) {
	// 활성 모델만 세면 안 됩니다. 모델을 비활성으로 바꿔도 Qdrant 컬렉션은
	// 남아 메모리를 그대로 씁니다. 세지 않으면 실제보다 넉넉하게 나옵니다.
	models := active
	if all, err := rt.store.AllModels(ctx); err != nil {
		rt.log.Warn("등록된 모델을 다 읽지 못해 활성 모델로만 셉니다",
			slog.String("error", err.Error()))
	} else if len(all) > 0 {
		models = all
	}

	sizes := make([]int, 0, len(models))
	for _, m := range models {
		sizes = append(sizes, m.VectorSize)
	}
	// 납작한 색인은 메모리가 장수를 제한하지 않습니다. 걸어 두기만 하므로
	// 모자라면 죽는 대신 느려집니다. 겁줄 일이 아니라 디스크를 알려 줍니다.
	if rt.cfg.IndexKind != domain.IndexQdrant {
		perDisk := app.IndexDiskBytesPerImage(sizes)
		if perDisk <= 0 {
			return
		}
		rt.log.Info("납작한 색인이라 메모리가 담을 장수를 제한하지 않습니다",
			slog.Int("컬렉션 수", len(sizes)),
			slog.Int64("장당 디스크 바이트", perDisk),
			slog.String("1,190만 장이면", humanBytes(11_900_000*perDisk)),
			slog.String("색인 위치", rt.cfg.IndexDir))
		return
	}

	perImage := app.IndexBytesPerImage(domain.IndexQdrant, sizes)
	if perImage <= 0 {
		return
	}

	if rt.cfg.MaxIndexed > 0 {
		rt.log.Info("담을 장수를 정해 두었습니다",
			slog.Int("컬렉션 수", len(sizes)),
			slog.Int64("상한", rt.cfg.MaxIndexed),
			slog.Int64("장당 메모리 바이트", perImage),
			slog.String("필요한 메모리", humanBytes(rt.cfg.MaxIndexed*perImage)))
		return
	}

	total := totalMemoryBytes()
	if total <= 0 {
		rt.log.Warn("담을 장수를 정하지 않았습니다. 차면 Qdrant가 죽습니다",
			slog.Int64("장당 메모리 바이트", perImage),
			slog.String("정하려면", "SAUCEDUST_MAX_INDEXED"))
		return
	}

	fits := app.ImageCapacity(qdrantShareBytes(total), sizes)
	rt.log.Warn("담을 장수를 정하지 않았습니다. 차면 Qdrant가 죽습니다",
		slog.String("이 컴퓨터 메모리", humanBytes(total)),
		slog.Int64("들어갈 만한 장수", fits),
		slog.String("정하려면", "SAUCEDUST_MAX_INDEXED"))
}

func humanBytes(n int64) string {
	const unit = 1 << 10
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	value, exp := float64(n), 0
	for value >= unit && exp < 4 {
		value /= unit
		exp++
	}
	return strconv.FormatFloat(value, 'f', 1, 64) + " " +
		[...]string{"B", "KB", "MB", "GB", "TB"}[exp]
}
