package audio

import (
	"errors"
	"math"
	"time"
)

// 混响参数校验错误，错误信息中携带参数名，便于定位。
var (
	// ErrDecayTimeZero 衰减时长 decayTime 为零（或非正）。
	ErrDecayTimeZero = errors.New("参数 decayTime(衰减时长) 必须为正，不能为零")
	// ErrDecayZero 尾端衰减电平 decayLevel 为零，零电平无法反解有限的衰减系数。
	ErrDecayZero = errors.New("参数 decayLevel(尾端衰减电平) 不能为零")
	// ErrDecayLevel 尾端衰减电平超出 (0,1] 区间。
	ErrDecayLevel = errors.New("参数 decayLevel(尾端衰减电平) 必须落在 (0,1] 区间")
	// ErrRateMismatch 混响器采样率与音频片段采样率不一致。
	ErrRateMismatch = errors.New("参数 rate(采样率) 不一致: 混响器与音频片段采样率不同")
)

// Reverb 指数衰减尾音卷积混响器。
//
// 反解关系（τ=衰减时间，level=尾端相对电平）：
//
//	L = round(τ * rate)              尾音长度（样本数）
//	α = level^(1/(L-1))              衰减系数，满足 α^(L-1) = level
//	h[n] = α^n, n = 0..L-1           脉冲响应
//	g = 1 / sqrt(Σ h[n]²)            按衰减系数计算的能量守恒增益
//
// 归一化后核能量 Σ(g·h[n])² = 1，单位脉冲输入卷积前后能量比严格为 1。
type Reverb struct {
	Rate       int
	DecayTime  float64 // 衰减时间 τ，秒
	DecayLevel float64 // 尾端相对电平 level，(0,1]
	Enabled    bool    // 未启用时 Process 逐样本原样输出

	TailSamples int       // 反解出的尾音长度 L（样本数）
	Alpha       float64   // 反解出的衰减系数 α
	Kernel      []float64 // 归一化后的单位能量脉冲响应
}

// NewReverb 按衰减时间与尾端电平反解尾音长度和衰减系数，并构造单位能量卷积核。
func NewReverb(rate int, decayTime, decayLevel float64, enabled bool) (*Reverb, error) {
	if decayTime <= 0 {
		return nil, ErrDecayTimeZero
	}
	if decayLevel == 0 {
		return nil, ErrDecayZero
	}
	if decayLevel < 0 || decayLevel > 1 {
		return nil, ErrDecayLevel
	}
	if rate <= 0 {
		return nil, ErrSampleRate
	}

	L := int(math.Round(decayTime * float64(rate)))
	if L < 1 {
		L = 1
	}

	alpha := 1.0
	if L > 1 {
		alpha = math.Pow(decayLevel, 1.0/float64(L-1))
	}

	r := &Reverb{
		Rate:        rate,
		DecayTime:   decayTime,
		DecayLevel:  decayLevel,
		Enabled:     enabled,
		TailSamples: L,
		Alpha:       alpha,
	}

	// 构造指数脉冲响应并按衰减系数归一化为单位能量。
	kernel := make([]float64, L)
	var energy float64
	for n := 0; n < L; n++ {
		v := math.Pow(alpha, float64(n))
		kernel[n] = v
		energy += v * v
	}
	gain := 1.0 / math.Sqrt(energy)
	kernelEnergy := 0.0
	for n := range kernel {
		kernel[n] *= gain
		kernelEnergy += kernel[n] * kernel[n]
	}
	r.Kernel = kernel
	_ = kernelEnergy // 测试中通过 KernelEnergy() 校验
	return r, nil
}

// TailDuration 尾音长度对应的物理时长。
func (r *Reverb) TailDuration() time.Duration {
	return time.Duration(float64(r.TailSamples) / float64(r.Rate) * float64(time.Second))
}

// KernelEnergy 返回归一化卷积核的总能量，不变量目标值为 1。
func (r *Reverb) KernelEnergy() float64 {
	var e float64
	for _, v := range r.Kernel {
		e += v * v
	}
	return e
}

// Process 对片段做全长度卷积（输出 len(x)+L-1 帧，包含补出的尾音）。
// 未启用时逐样本原样输出，长度与输入完全一致；采样率不一致返回 ErrRateMismatch。
func (r *Reverb) Process(c *Clip) (*Clip, error) {
	if !r.Enabled {
		out := &Clip{Rate: c.Rate, Frames: make([]int16, len(c.Frames))}
		copy(out.Frames, c.Frames)
		return out, nil
	}
	if c.Rate != r.Rate {
		return nil, ErrRateMismatch
	}

	x := make([]float64, len(c.Frames))
	for i, v := range c.Frames {
		x[i] = float64(v)
	}
	y := ConvolveFFT(x, r.Kernel)

	frames := make([]int16, len(y))
	for i, v := range y {
		frames[i] = clampInt16(v)
	}
	return &Clip{Rate: c.Rate, Frames: frames}, nil
}

// ProcessFloat 与 Process 相同，但返回 int16 钳位之前的浮点卷积结果，
// 用于在浮点域核算能量不变量，避免量化截断污染能量比。
func (r *Reverb) ProcessFloat(c *Clip) ([]float64, error) {
	if !r.Enabled {
		out := make([]float64, len(c.Frames))
		for i, v := range c.Frames {
			out[i] = float64(v)
		}
		return out, nil
	}
	if c.Rate != r.Rate {
		return nil, ErrRateMismatch
	}
	x := make([]float64, len(c.Frames))
	for i, v := range c.Frames {
		x[i] = float64(v)
	}
	return ConvolveFFT(x, r.Kernel), nil
}

// Energy 计算浮点信号的总能量 Σx²。
func Energy(s []float64) float64 {
	var e float64
	for _, v := range s {
		e += v * v
	}
	return e
}

// FramesToFloat 把 int16 帧转浮点，能量核算统一在浮点域进行。
func FramesToFloat(frames []int16) []float64 {
	out := make([]float64, len(frames))
	for i, v := range frames {
		out[i] = float64(v)
	}
	return out
}

func clampInt16(v float64) int16 {
	if v >= 32767 {
		return 32767
	}
	if v <= -32768 {
		return -32768
	}
	return int16(math.Round(v))
}

// ConvolveNaive 朴素 O(N·L) 卷积，仅用于与 FFT 卷积交叉验证。
func ConvolveNaive(x, h []float64) []float64 {
	out := make([]float64, len(x)+len(h)-1)
	for i, xv := range x {
		for j, hv := range h {
			out[i+j] += xv * hv
		}
	}
	return out
}

// ConvolveFFT 基 2 FFT 线性卷积，零填充到不小于 N+L-1 的最小 2 的幂。
func ConvolveFFT(x, h []float64) []float64 {
	outN := len(x) + len(h) - 1
	n := 1
	for n < outN {
		n <<= 1
	}
	a := make([]complex128, n)
	b := make([]complex128, n)
	for i, v := range x {
		a[i] = complex(v, 0)
	}
	for i, v := range h {
		b[i] = complex(v, 0)
	}
	fft(a, false)
	fft(b, false)
	for i := range a {
		a[i] *= b[i]
	}
	fft(a, true)
	out := make([]float64, outN)
	for i := 0; i < outN; i++ {
		out[i] = real(a[i])
	}
	return out
}

// fft 迭代式基 2 Cooley-Tukey，invert 为 true 时做逆变换并按 1/n 缩放。
func fft(a []complex128, invert bool) {
	n := len(a)
	// 位反转置换
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	for length := 2; length <= n; length <<= 1 {
		ang := 2 * math.Pi / float64(length)
		if invert {
			ang = -ang
		}
		wlen := complex(math.Cos(ang), math.Sin(ang))
		for i := 0; i < n; i += length {
			w := complex(1, 0)
			half := length / 2
			for j := 0; j < half; j++ {
				u := a[i+j]
				v := a[i+j+half] * w
				a[i+j] = u + v
				a[i+j+half] = u - v
				w *= wlen
			}
		}
	}
	if invert {
		inv := complex(1.0/float64(n), 0)
		for i := range a {
			a[i] *= inv
		}
	}
}
