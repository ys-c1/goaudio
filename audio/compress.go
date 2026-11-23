package audio

import (
	"fmt"
	"math"
	"time"
)

// LevelFloorDB 是电平检测的下限（dBFS）。静音或极弱信号按此值处理，
// 避免对零取对数。
const LevelFloorDB = -120.0

// dbFSRef 是 int16 满幅对应的幅度参考。
const dbFSRef = 32768.0

// Config 描述压限器参数。
//
// ThresholdDB 为门限（dBFS，必须为负）；Ratio 为压缩比（必须为正）；
// KneeDB 为软拐点总宽度（dB），0 表示沿用原有硬拐点；Duration 为电平
// 检测窗时长（必须为正）。
type Config struct {
	ThresholdDB float64
	Ratio       float64
	KneeDB      float64
	Duration    time.Duration
}

// ConfigError 指出具体是哪一个字段非法。
type ConfigError struct {
	Field string
	Value float64
	Err   error
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("参数 %s 非法（%g）: %v", e.Field, e.Value, e.Err)
}

func (e *ConfigError) Unwrap() error { return e.Err }

// Validate 校验压限参数。门限为零、比率为负、时长为零都会返回
// 带字段名的错误。
func (c Config) Validate() error {
	if math.IsNaN(c.ThresholdDB) || math.IsInf(c.ThresholdDB, 0) || c.ThresholdDB >= 0 {
		return &ConfigError{Field: "threshold", Value: c.ThresholdDB, Err: ErrThreshold}
	}
	if math.IsNaN(c.Ratio) || math.IsInf(c.Ratio, 0) || c.Ratio <= 0 {
		return &ConfigError{Field: "ratio", Value: c.Ratio, Err: ErrRatio}
	}
	if math.IsNaN(c.KneeDB) || math.IsInf(c.KneeDB, 0) || c.KneeDB < 0 {
		return &ConfigError{Field: "knee", Value: c.KneeDB, Err: ErrKnee}
	}
	if c.Duration <= 0 {
		return &ConfigError{Field: "duration", Value: c.Duration.Seconds(), Err: ErrDuration}
	}
	return nil
}

// HardKneeGainDB 是原有硬拐点接口：输入电平不超过门限时不压缩，
// 超过门限后按固定压缩比衰减。返回值为增益（dB，≤ 0）。
//
// 该接口的语义保持不变；软拐点路径在宽度为零时会原样退回这里。
func HardKneeGainDB(inputDB, thresholdDB, ratio float64) float64 {
	if inputDB <= thresholdDB {
		return 0
	}
	return (1/ratio - 1) * (inputDB - thresholdDB)
}

// HardKneeOutputDB 返回硬拐点下的输出电平（dBFS）。
func HardKneeOutputDB(inputDB, thresholdDB, ratio float64) float64 {
	return inputDB + HardKneeGainDB(inputDB, thresholdDB, ratio)
}

// SoftKneeGainDB 返回软拐点下的增益（dB，≤ 0）。
//
// 拐点区为 [threshold-knee/2, threshold+knee/2]，曲线在两侧边界上
// 取值与一阶导数均与线性段相接（C1 连续）：
//
//	左段 x ≤ T-W/2:  g = 0
//	拐点区:          g = (1/R-1)·(x-T+W/2)²/(2W)
//	右段 x ≥ T+W/2:  g = (1/R-1)·(x-T)
//
// 拐点处曲率为 (1/R-1)/W。宽度为 0 时退回 HardKneeGainDB。
func SoftKneeGainDB(inputDB, thresholdDB, ratio, kneeDB float64) float64 {
	if kneeDB <= 0 {
		return HardKneeGainDB(inputDB, thresholdDB, ratio)
	}
	a := 1/ratio - 1
	lo := thresholdDB - kneeDB/2
	hi := thresholdDB + kneeDB/2
	switch {
	case inputDB <= lo:
		return 0
	case inputDB >= hi:
		return a * (inputDB - thresholdDB)
	default:
		d := inputDB - thresholdDB + kneeDB/2
		return a * d * d / (2 * kneeDB)
	}
}

// SoftKneeOutputDB 返回软拐点下的输出电平（dBFS）。
func SoftKneeOutputDB(inputDB, thresholdDB, ratio, kneeDB float64) float64 {
	return inputDB + SoftKneeGainDB(inputDB, thresholdDB, ratio, kneeDB)
}

// SoftKneeSlope 返回压缩曲线 dy/dx 在输入电平处的值（dB/dB）。
// 软拐点两侧斜率分别为 1（门限以下）与 1/R（门限以上），
// 拐点区内介于两者之间。
func SoftKneeSlope(inputDB, thresholdDB, ratio, kneeDB float64) float64 {
	if kneeDB <= 0 {
		if inputDB <= thresholdDB {
			return 1
		}
		return 1 / ratio
	}
	a := 1/ratio - 1
	lo := thresholdDB - kneeDB/2
	hi := thresholdDB + kneeDB/2
	switch {
	case inputDB < lo:
		return 1
	case inputDB > hi:
		return 1 / ratio
	default:
		return 1 + a*(inputDB-lo)/kneeDB
	}
}

// SoftKneeCurvature 返回软拐点区的二阶导数 d²y/dx²（1/dB）。
// 拐点区内为常数；宽度为零（硬拐点）时无定义，返回 0。
func SoftKneeCurvature(ratio, kneeDB float64) float64 {
	if kneeDB <= 0 {
		return 0
	}
	return (1/ratio - 1) / kneeDB
}

// Result 是一次压限处理的结果。GainDB 是逐帧增益曲线，
// InputDB 是逐帧检测到的输入电平，OutputDB 为按静态曲线推算的
// 输出电平（InputDB + GainDB），Frames 为处理后音频。
type Result struct {
	InputDB  []float64
	GainDB   []float64
	OutputDB []float64
	Frames   []int16
}

// Process 按给定参数处理整段音频。检测窗为因果滑动 RMS 窗；
// 处理过程完全确定，同一素材重复运行得到逐位相同的增益曲线。
func Process(c *Clip, cfg Config) (*Result, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if c == nil {
		return nil, ErrClip
	}
	n := len(c.Frames)
	if n == 0 {
		return nil, &ConfigError{Field: "frames", Value: 0, Err: ErrFrames}
	}
	window := levelWindowFrames(c.Rate, cfg.Duration)
	levels := frameLevels(c.Frames, window)

	res := &Result{
		InputDB:  levels,
		GainDB:   make([]float64, n),
		OutputDB: make([]float64, n),
		Frames:   make([]int16, n),
	}
	for i, v := range c.Frames {
		g := SoftKneeGainDB(levels[i], cfg.ThresholdDB, cfg.Ratio, cfg.KneeDB)
		res.GainDB[i] = g
		res.OutputDB[i] = levels[i] + g
		lin := math.Pow(10, g/20)
		s := math.Round(float64(v) * lin)
		switch {
		case s > 32767:
			s = 32767
		case s < -32768:
			s = -32768
		}
		res.Frames[i] = int16(s)
	}
	return res, nil
}

// levelWindowFrames 把检测时长换算成帧数，至少一帧。
func levelWindowFrames(rate int, d time.Duration) int {
	w := int(math.Round(float64(rate) * d.Seconds()))
	if w < 1 {
		w = 1
	}
	return w
}

// frameLevels 计算因果滑动 RMS 电平（dBFS）。
func frameLevels(frames []int16, window int) []float64 {
	n := len(frames)
	prefix := make([]float64, n+1)
	for i, v := range frames {
		x := float64(v)
		prefix[i+1] = prefix[i] + x*x
	}
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		start := i - window + 1
		if start < 0 {
			start = 0
		}
		count := i - start + 1
		mean := (prefix[i+1] - prefix[start]) / float64(count)
		out[i] = meanSquareDB(mean)
	}
	return out
}

func meanSquareDB(ms float64) float64 {
	if ms <= 0 {
		return LevelFloorDB
	}
	return 20 * math.Log10(math.Sqrt(ms)/dbFSRef)
}

func rmsDB(frames []int16) float64 {
	if len(frames) == 0 {
		return LevelFloorDB
	}
	var sum float64
	for _, v := range frames {
		x := float64(v)
		sum += x * x
	}
	return meanSquareDB(sum / float64(len(frames)))
}

// LevelPair 是一组实测的输入/输出电平对应（dBFS）。
type LevelPair struct {
	InputDB  float64
	OutputDB float64
}

// sinePeakForRMSLevel 返回 RMS 电平为 levelDB 的正弦峰值幅度。
// 正弦 RMS 比峰值低 3.0103 dB。
func sinePeakForRMSLevel(levelDB float64) int {
	const sineRMSOffset = 3.010299956639812
	amp := math.Round(dbFSRef * math.Pow(10, (levelDB+sineRMSOffset)/20))
	if amp < 1 {
		amp = 1
	}
	if amp > 32767 {
		amp = 32767
	}
	return int(amp)
}

// MeasureLevelPairs 用稳态正弦逐点测出压限器的输入输出电平对应。
//
// 每个输入电平生成一段正弦，处理后在检测窗稳定处取输出 RMS；
// 输入电平也取同一段尾窗的实测 RMS，保证横坐标来自实测而非设定值。
// seconds 至少应覆盖四个检测窗。
func MeasureLevelPairs(levels []float64, cfg Config, rate int, seconds float64) ([]LevelPair, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if rate <= 0 {
		return nil, &ConfigError{Field: "rate", Value: float64(rate), Err: ErrSampleRate}
	}
	if len(levels) == 0 {
		return nil, ErrLevels
	}
	window := levelWindowFrames(rate, cfg.Duration)
	n := int(math.Round(seconds * float64(rate)))
	if n < 4*window || n < 8 {
		return nil, &ConfigError{Field: "seconds", Value: seconds, Err: ErrMeasure}
	}
	// 探针频率取检测窗的整数个周期，使稳态 RMS 无纹波。
	freq := float64(rate) / float64(window)
	if freq >= float64(rate)/2 || freq <= 0 {
		freq = float64(rate) / 8
	}
	if freq <= 0 || freq >= float64(rate)/2 {
		return nil, &ConfigError{Field: "rate", Value: float64(rate), Err: ErrSampleRate}
	}
	pairs := make([]LevelPair, 0, len(levels))
	for _, level := range levels {
		src, err := NewClip(rate, n)
		if err != nil {
			return nil, err
		}
		if err := Sine(src, freq, sinePeakForRMSLevel(level)); err != nil {
			return nil, err
		}
		out, err := Process(src, cfg)
		if err != nil {
			return nil, err
		}
		tail := n - window
		pairs = append(pairs, LevelPair{
			InputDB:  rmsDB(src.Frames[tail:]),
			OutputDB: rmsDB(out.Frames[tail:]),
		})
	}
	return pairs, nil
}
