package audio

import (
	"math"
	"time"
)

// CompressorConfig 软拐点压缩器配置。
//
// 全部参数与信号量纲一致（int16 线性幅度，非 dB）：
//   - Threshold：门限（膝中心），线性幅度，合法范围 1..32767
//   - Ratio：压缩比率 R:1，必须 >= 0；R=0 按 ∞:1 限幅器处理
//   - Knee：软拐点宽度，单位 dB，必须 >= 0；0 表示硬拐点
//   - AttackMs / ReleaseMs：包络时间常数，单位毫秒，必须 >= 0
//   - Enabled：false 时 Compress 仅逐样本拷贝，输出与输入完全一致
type CompressorConfig struct {
	Threshold int
	Ratio     float64
	KneeDB    float64
	AttackMs  float64
	ReleaseMs float64
	Enabled   bool
}

// CompressReport 一次压缩处理的汇总结果。
type CompressReport struct {
	InputPeak     int           // 输入峰值（线性幅度绝对值）
	OutputPeak    int           // 输出峰值（线性幅度绝对值）
	MaxGainReduce float64       // 最大增益衰减（dB，正数表示衰减量）
	MeanGainDB    float64       // 全片段平均增益（dB）
	Elapsed       time.Duration // 处理耗时
}

const (
	// 包络状态保持不更新的输入电平（线性，静音）。
	envFloorLinear = 1.0 / 32768.0
	envFloorDB     = -90.33 // 20*log10(1/32768)
)

// Compress 按配置对 src 做软拐点压缩，结果写入 dst。
//
// Enabled=false 时不做任何压缩参数校验，逐样本拷贝（dst 与 src 等长时输出逐样本不变）；
// 但 src/dst 的结构性不匹配（采样率或帧数）仍会返回错误，避免越界。
// 参数错误分别返回 ErrThreshold / ErrRatio / ErrDuration，调用方可 errors.Is 判别。
func Compress(dst, src *Clip, cfg CompressorConfig) (*CompressReport, error) {
	start := time.Now()
	if dst.Rate != src.Rate {
		return nil, ErrRateMismatch
	}
	if len(dst.Frames) != len(src.Frames) {
		return nil, ErrSizeMismatch
	}
	if !cfg.Enabled {
		for i, v := range src.Frames {
			dst.Frames[i] = v
		}
		return &CompressReport{
			InputPeak:  Peak(src),
			OutputPeak: Peak(dst),
			Elapsed:    time.Since(start),
		}, nil
	}

	// ---- 参数校验：错误码各自指明门限 / 比率 / 时长 ----
	if cfg.Threshold <= 0 || cfg.Threshold > 32767 {
		return nil, ErrThreshold
	}
	if math.IsNaN(cfg.Ratio) || cfg.Ratio < 0 {
		return nil, ErrRatio
	}
	if cfg.KneeDB < 0 || math.IsNaN(cfg.KneeDB) ||
		cfg.AttackMs < 0 || math.IsNaN(cfg.AttackMs) ||
		cfg.ReleaseMs < 0 || math.IsNaN(cfg.ReleaseMs) {
		// 门限之外的标量参数（膝宽/攻击/释放）非法，归入比率类参数错误码。
		return nil, ErrRatio
	}
	attackFrames := msToFrames(cfg.AttackMs, src.Rate)
	releaseFrames := msToFrames(cfg.ReleaseMs, src.Rate)
	n := len(src.Frames)
	if n < 2 || n <= attackFrames+releaseFrames {
		return nil, ErrDuration
	}

	threshDB := ampToDB(float64(cfg.Threshold))
	kneeHalf := cfg.KneeDB / 2
	attackCoeff := timeConstant(cfg.AttackMs, src.Rate)
	releaseCoeff := timeConstant(cfg.ReleaseMs, src.Rate)
	// 前瞻帧数等于攻击时间常数：增益提前看到即将到来的峰值，
	// 避免首峰在包络建立前原样穿过（“大声压不下去”）。
	lookahead := attackFrames

	// ---- 第一遍：包络跟随器 + 静态曲线，逐帧求线性增益 ----
	env := envFloorLinear
	maxReduce := 0.0
	gainSum := 0.0
	gains := make([]float64, n)
	for i := 0; i < n; i++ {
		detector := math.Abs(float64(src.Frames[i]))
		if detector < envFloorLinear {
			detector = envFloorLinear
		}
		coeff := attackCoeff
		if detector < env {
			coeff = releaseCoeff
		}
		env = (1-coeff)*env + coeff*detector

		gainDB := softKneeGainDB(ampToDB(env), threshDB, kneeHalf, cfg.Ratio)
		gains[i] = math.Pow(10, gainDB/20)
		if -gainDB > maxReduce {
			maxReduce = -gainDB
		}
		gainSum += gainDB
	}

	// ---- 第二遍：前瞻施加增益。out[i] 用第 min(i+L, n-1) 帧的增益，
	// 末尾 L 帧冻结最后一帧增益；输出长度不变。 ----
	for i := 0; i < n; i++ {
		j := i + lookahead
		if j >= n {
			j = n - 1
		}
		y := float64(src.Frames[i]) * gains[j]
		// 与既有代码一致的 int16 饱和语义，防止极端舍入越界。
		iv := int(math.Round(y))
		if iv > 32767 {
			iv = 32767
		} else if iv < -32768 {
			iv = -32768
		}
		dst.Frames[i] = int16(iv)
	}

	return &CompressReport{
		InputPeak:     Peak(src),
		OutputPeak:    Peak(dst),
		MaxGainReduce: maxReduce,
		MeanGainDB:    gainSum / float64(n),
		Elapsed:       time.Since(start),
	}, nil
}

// softKneeGainDB 静态软拐点增益曲线（dB 输入 -> dB 增益）。
//
// 以门限 T 为中心、半宽 W=KneeDB/2 分三段：
//  1. x <= T-W：        不压缩，增益 0
//  2. |x-T| < W：       软拐点，增益 = -(1-1/R)*(x-T+W)^2/(4W)；W=0 时此段消失
//  3. x >= T+W：        全比率压缩，增益 = -(1-1/R)*(x-T)
//
// ratio=0 视为限幅器（1/R=0）。
func softKneeGainDB(x, thresh, kneeHalf, ratio float64) float64 {
	inv := 0.0
	if ratio > 0 {
		inv = 1 / ratio
	}
	slope := 1 - inv
	d := x - thresh
	if kneeHalf <= 0 {
		if d > 0 {
			return -slope * d
		}
		return 0
	}
	switch {
	case d <= -kneeHalf:
		return 0
	case d >= kneeHalf:
		return -slope * d
	default:
		return -slope * (d + kneeHalf) * (d + kneeHalf) / (4 * kneeHalf)
	}
}

// StaticGainCurve 返回一组静态增益曲线点，供手算对照与制表。
// 输入点以 dBFS 给出（0 为满幅），返回每个点的输入 dB 与增益 dB。
func StaticGainCurve(pointsDB []float64, cfg CompressorConfig) [][2]float64 {
	threshDB := ampToDB(float64(cfg.Threshold))
	kneeHalf := cfg.KneeDB / 2
	out := make([][2]float64, len(pointsDB))
	for i, x := range pointsDB {
		out[i] = [2]float64{x, softKneeGainDB(x, threshDB, kneeHalf, cfg.Ratio)}
	}
	return out
}

func ampToDB(a float64) float64 {
	if a <= 0 {
		return envFloorDB
	}
	return 20 * math.Log10(a/32768.0)
}

// msToFrames 毫秒时长对应的帧数（四舍五入）。
func msToFrames(ms float64, rate int) int {
	return int(math.Round(ms / 1000 * float64(rate)))
}

// timeConstant 一阶低通系数：coeff = 1 - exp(-1/(fs*tau))，
// tau 由毫秒换算。tau=0 时系数为 1（瞬时跟随）。
func timeConstant(ms float64, rate int) float64 {
	if ms <= 0 {
		return 1
	}
	tauFrames := ms / 1000 * float64(rate)
	return 1 - math.Exp(-1/tauFrames)
}
