package audio

import (
	"fmt"
	"math"
	"math/cmplx"
)

// PitchShift 变调不变速：把当前主频搬到 targetFreq，输出时长与输入严格相同。
//
// 流程：按帧做频域搬移（幅度谱按 ratio 映射到新频点），再做相位累积修正
// 保证帧间连续，最后按原始帧长重叠相加（帧移不变，所以时长不变）。
//
// 为什么不能只搬移幅度谱：相位是频率对时间的积分。某个分量被搬到新频点后，
// 它的相位每帧必须按“新频率 × 帧移”推进，相邻帧在重叠区才能同相叠加。
// 若只搬幅度、相位照搬旧值（或清零），分量的瞬时频率仍是旧值，帧间相位
// 对不上，重叠相加时互相抵消，得到的是幅度抖动和发虚的“phasiness”，
// 而不是干净的变调。
func PitchShift(c *Clip, targetFreq float64, frameSize int) error {
	if c.Rate <= 0 {
		return fmt.Errorf("采样率: %w", ErrSampleRate)
	}
	if frameSize <= 0 {
		return fmt.Errorf("帧长: %w", ErrFrameSize)
	}
	if targetFreq <= 0 || targetFreq >= float64(c.Rate)/2 {
		return fmt.Errorf("目标频率: %w", ErrFreq)
	}
	n := len(c.Frames)
	if n == 0 {
		return nil
	}
	src := make([]float64, n)
	for i, v := range c.Frames {
		src[i] = float64(v)
	}
	f0 := dominantFreq(src, c.Rate)
	if f0 <= 0 {
		return nil // 静音等测不到主频的输入保持不变
	}
	out := vocoderShift(src, f0, targetFreq, frameSize)
	for i := range c.Frames {
		v := math.Round(out[i])
		if v > 32767 {
			v = 32767
		}
		if v < -32768 {
			v = -32768
		}
		c.Frames[i] = int16(v)
	}
	return nil
}

// vocoderShift 相位声码器变调：ratio = targetFreq / srcFreq，帧长 frameSize。
func vocoderShift(x []float64, srcFreq, targetFreq float64, frameSize int) []float64 {
	ratio := targetFreq / srcFreq
	n := len(x)

	N := nextPow2(frameSize)
	if N < 8 {
		N = 8
	}
	hop := N / 4 // 75% 重叠，帧移不变 → 时长不变
	win := hann(N)

	pad := N // 两端补零，保证首尾样本也被窗完全覆盖
	m := n + 2*pad
	buf := make([]float64, m)
	copy(buf[pad:], x)
	out := make([]float64, m)
	norm := make([]float64, m)

	bins := N/2 + 1
	prevPhase := make([]float64, bins) // 上一帧分析相位
	sumPhase := make([]float64, bins)  // 合成端累积相位
	magn := make([]float64, bins)
	anaFreq := make([]float64, bins) // 各 bin 瞬时角频率（rad/样本）
	synMagn := make([]float64, bins)
	synFreq := make([]float64, bins)
	frame := make([]complex128, N)

	hopF := float64(hop)
	first := true
	for pos := 0; pos+N <= m; pos += hop {
		for i := 0; i < N; i++ {
			frame[i] = complex(buf[pos+i]*win[i], 0)
		}
		fft(frame, false)

		// ---- 分析：相位展开，求每个 bin 的真实瞬时频率 ----
		// 第 m 帧第 k 个 bin 的相位 φ_m(k)。若分量真实角频率为 ω̂，则
		//   φ_m(k) − φ_{m−1}(k) = ω̂·hop + 2πq   (q 为未知整数)
		// 令 ω̂ = ω_k + Δ（ω_k = 2πk/N 为 bin 中心），则
		//   Δ·hop = wrap(φ_m − φ_{m−1} − ω_k·hop) ∈ (−π, π]
		// 只要 |Δ|·hop < π 缠绕唯一，可解出 Δ，进而 ω̂ = ω_k + Δ/hop。
		for k := 0; k < bins; k++ {
			re, im := real(frame[k]), imag(frame[k])
			magn[k] = math.Hypot(re, im)
			ph := math.Atan2(im, re)
			omegaK := 2 * math.Pi * float64(k) / float64(N)
			if first {
				anaFreq[k] = omegaK
				sumPhase[k] = ph
			} else {
				delta := ph - prevPhase[k] - omegaK*hopF
				delta = math.Remainder(delta, 2*math.Pi) // 缠绕到 (−π, π]
				anaFreq[k] = omegaK + delta/hopF
			}
			prevPhase[k] = ph
		}

		// ---- 频域搬移：bin k 的能量与瞬时频率一起搬到 round(ratio·k) ----
		for k := range synMagn {
			synMagn[k] = 0
			synFreq[k] = 0
		}
		for k := 0; k < bins; k++ {
			j := int(math.Round(float64(k) * ratio))
			if j >= 0 && j < bins {
				synMagn[j] += magn[k]
				synFreq[j] = anaFreq[k] * ratio
			}
		}

		// ---- 合成：相位按新频率累积，保证帧间连续 ----
		if !first {
			for k := 0; k < bins; k++ {
				sumPhase[k] += synFreq[k] * hopF
			}
		}
		for k := 0; k < bins; k++ {
			frame[k] = cmplx.Rect(synMagn[k], sumPhase[k])
		}
		for k := 1; k < N/2; k++ {
			frame[N-k] = cmplx.Conj(frame[k])
		}
		fft(frame, true)

		// ---- 按原始帧长重叠相加 ----
		for i := 0; i < N; i++ {
			out[pos+i] += real(frame[i]) * win[i]
			norm[pos+i] += win[i] * win[i]
		}
		first = false
	}

	res := make([]float64, n)
	for i := 0; i < n; i++ {
		w := norm[pad+i]
		if w > 1e-8 {
			res[i] = out[pad+i] / w
		}
	}
	return res
}

// dominantFreq 估计信号主频（Hz）；能量过低（如静音）返回 0。
func dominantFreq(x []float64, rate int) float64 {
	maxN := len(x)
	if maxN > 1<<16 {
		maxN = 1 << 16
	}
	n := 8
	for n*2 <= maxN {
		n *= 2
	}
	if n > maxN {
		return 0
	}
	win := hann(n)
	buf := make([]complex128, n)
	for i := 0; i < n; i++ {
		buf[i] = complex(x[i]*win[i], 0)
	}
	fft(buf, false)
	best, bestMag := 0, 0.0
	for k := 1; k <= n/2; k++ {
		if v := cmplx.Abs(buf[k]); v > bestMag {
			bestMag, best = v, k
		}
	}
	if bestMag < 1e-6 {
		return 0
	}
	// 抛物线插值细化峰值位置
	kf := float64(best)
	if best > 0 && best < n/2 {
		a := cmplx.Abs(buf[best-1])
		b := bestMag
		c := cmplx.Abs(buf[best+1])
		if d := a - 2*b + c; d != 0 {
			kf += 0.5 * (a - c) / d
		}
	}
	return kf * float64(rate) / float64(n)
}

func hann(n int) []float64 {
	w := make([]float64, n)
	for i := range w {
		w[i] = 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(n)))
	}
	return w
}

func nextPow2(v int) int {
	n := 1
	for n < v {
		n <<= 1
	}
	return n
}

// fft 迭代基-2 FFT；inverse 为 true 时做逆变换并除以 n。
func fft(a []complex128, inverse bool) {
	n := len(a)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j |= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		ang := 2 * math.Pi / float64(size)
		if !inverse {
			ang = -ang
		}
		w := cmplx.Rect(1, ang)
		half := size / 2
		for i := 0; i < n; i += size {
			wn := complex(1, 0)
			for j := 0; j < half; j++ {
				u := a[i+j]
				v := a[i+j+half] * wn
				a[i+j] = u + v
				a[i+j+half] = u - v
				wn *= w
			}
		}
	}
	if inverse {
		inv := complex(1/float64(n), 0)
		for i := range a {
			a[i] *= inv
		}
	}
}
