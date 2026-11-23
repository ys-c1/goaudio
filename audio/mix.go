package audio

import (
	"errors"
	"fmt"
	"math"
)

// MixConfig 混音总线配置：权重与软限幅门限均可配。
type MixConfig struct {
	// Weights 每路输入的权重，长度必须与 clips 相同；允许 0，不允许负值。
	Weights []float64
	// Threshold 软限幅门限，单位为 int16 幅值，取值 1..32767。
	Threshold int
}

// 逐类参数错误：错误信息中带参数名。
var (
	// ErrNoChannel 通道数为零。
	ErrNoChannel = errors.New("参数 channels: 通道数为零，至少需要一路输入")
	// ErrThreshold 门限非法。
	ErrThreshold = errors.New("参数 threshold: 门限必须在 1..32767 之间")
)

func errWeightsLen(got, want int) error {
	return fmt.Errorf("参数 weights: 权重数量 %d 与通道数 %d 不一致", got, want)
}

func errNegativeWeight(i int, w float64) error {
	return fmt.Errorf("参数 weights[%d]: 权重不能为负 (got %v)", i, w)
}

func errRateMismatch(i, got, want int) error {
	return fmt.Errorf("参数 clips[%d].Rate: 采样率不一致 (got %d, want %d)", i, got, want)
}

func errFramesMismatch(i, got, want int) error {
	return fmt.Errorf("参数 clips[%d].Frames: 帧数不一致 (got %d, want %d)", i, got, want)
}

// validateMix 汇总混音入参校验，返回基准 clip 与统一帧数。
func validateMix(clips []*Clip, weights []float64, threshold int) (*Clip, int, error) {
	n := len(clips)
	if n == 0 {
		return nil, 0, ErrNoChannel
	}
	if len(weights) != n {
		return nil, 0, errWeightsLen(len(weights), n)
	}
	for i, w := range weights {
		if w < 0 {
			return nil, 0, errNegativeWeight(i, w)
		}
	}
	if threshold < 1 || threshold > 32767 {
		return nil, 0, ErrThreshold
	}
	base := clips[0]
	for i := 1; i < n; i++ {
		if clips[i].Rate != base.Rate {
			return nil, 0, errRateMismatch(i, clips[i].Rate, base.Rate)
		}
	}
	frames := len(base.Frames)
	for i := 1; i < n; i++ {
		if len(clips[i].Frames) != frames {
			return nil, 0, errFramesMismatch(i, len(clips[i].Frames), frames)
		}
	}
	return base, frames, nil
}

// int16 满量程天花板：软限幅输出恒严格小于该值，不需要硬削顶。
const softCeiling = 32767.0

// SoftLimit 软限幅。
// 门限内为恒等线性区（|s| <= T 时原样返回），保证未触发限幅时逐样本不变；
// 超门限进入指数软拐点，门限处值与一阶导数均连续（C1），
// 随 |s|→∞ 从上方渐近饱和到满量程天花板 C=32767，输出恒满足 T < |y| < C，
// 因此不会削顶、不会溢出 int16。T=C（门限开满）时退化为门限处饱和。
func SoftLimit(s float64, threshold int) float64 {
	t := float64(threshold)
	abs := math.Abs(s)
	if abs <= t {
		return s
	}
	if t >= softCeiling {
		if s < 0 {
			return -softCeiling
		}
		return softCeiling
	}
	knee := softCeiling - t
	u := abs - t
	y := t + knee*(1-math.Exp(-u/knee))
	if s < 0 {
		return -y
	}
	return y
}

// quantize16 四舍五入量化到 int16，超范围做硬保护（兜底；软限幅正常不会触发）。
func quantize16(y float64) int16 {
	v := math.Round(y)
	if v >= 32767 {
		return 32767
	}
	if v <= -32768 {
		return -32768
	}
	return int16(v)
}

// PreLimitPeak 限幅前加权和（连续域）的峰值绝对值，用于判定限幅是否介入。
func PreLimitPeak(clips []*Clip, weights []float64) (float64, error) {
	_, frames, err := validateMix(clips, weights, 32767)
	if err != nil {
		return 0, err
	}
	sum := weightedSum(clips, weights, frames)
	peak := 0.0
	for _, s := range sum {
		if a := math.Abs(s); a > peak {
			peak = a
		}
	}
	return peak, nil
}

// weightedSum 浮点加权求和，返回限幅前的连续域信号。
func weightedSum(clips []*Clip, weights []float64, frames int) []float64 {
	s := make([]float64, frames)
	for i := range clips {
		w := weights[i]
		if w == 0 {
			continue
		}
		for k := 0; k < frames; k++ {
			s[k] += w * float64(clips[i].Frames[k])
		}
	}
	return s
}

// NaiveMix 限幅前的加权求和直接量化（旧行为：超出 int16 即硬削顶），
// 作为能量守恒对照中的“限幅前”检查点。
func NaiveMix(clips []*Clip, weights []float64) (*Clip, error) {
	base, frames, err := validateMix(clips, weights, 32767)
	if err != nil {
		return nil, err
	}
	sum := weightedSum(clips, weights, frames)
	out := &Clip{Rate: base.Rate, Frames: make([]int16, frames)}
	for k, s := range sum {
		out.Frames[k] = quantize16(s)
	}
	return out, nil
}

// MixBus 混音总线：按权重求和后做软限幅。
// 门限内信号恒等通过，因此单路输入（权重 1、门限不低于其峰值）输出逐帧不变。
func MixBus(clips []*Clip, cfg MixConfig) (*Clip, error) {
	base, frames, err := validateMix(clips, cfg.Weights, cfg.Threshold)
	if err != nil {
		return nil, err
	}
	sum := weightedSum(clips, cfg.Weights, frames)
	out := &Clip{Rate: base.Rate, Frames: make([]int16, frames)}
	for k, s := range sum {
		out.Frames[k] = quantize16(SoftLimit(s, cfg.Threshold))
	}
	return out, nil
}

// Energy 信号能量 E = Σ x[n]^2，按 float64 累加。
func Energy(c *Clip) float64 {
	var e float64
	for _, v := range c.Frames {
		f := float64(v)
		e += f * f
	}
	return e
}

// WeightedInputEnergy 混音前各加权分量的能量合计 Σ_i w_i^2 · E(x_i)。
func WeightedInputEnergy(clips []*Clip, weights []float64) float64 {
	var e float64
	for i := range clips {
		w := weights[i]
		if w == 0 {
			continue
		}
		e += w * w * Energy(clips[i])
	}
	return e
}
