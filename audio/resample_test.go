package audio

import (
	"errors"
	"math"
	"testing"
)

// 测试信号：三个整周期正弦叠加，频率在各采样率下都对齐 DFT 栅格，
// 因此有限长信号无泄漏，密集采样参考谱可视为真值。
var (
	testFreqs = []float64{292.96875, 976.5625, 2343.75} // 150/500/1200 × 16000/8192
	testAmps  = []float64{9000, 6000, 3000}
)

func multiSine(c *Clip, freqs, amps []float64) {
	for i := range c.Frames {
		t := float64(i) / float64(c.Rate)
		v := 0.0
		for k := range freqs {
			v += amps[k] * math.Sin(2*math.Pi*freqs[k]*t)
		}
		c.Frames[i] = int16(math.Round(v))
	}
}

// denseRefMag 以 16 倍采样率合成同一连续信号并求幅度谱，
// 作为“密集采样参考谱”。参考谱频率分辨率与输出谱相同。
func denseRefMag(freqs, amps []float64, outRate, outFFT int) []float64 {
	refRate := outRate * 16
	n := outFFT * 16
	x := make([]float64, n)
	for i := 0; i < n; i++ {
		t := float64(i) / float64(refRate)
		for k := range freqs {
			x[i] += amps[k] * math.Sin(2*math.Pi*freqs[k]*t)
		}
	}
	return fftMag(x)
}

// spectralError 返回输出归一化幅度谱与密集采样参考谱的最大偏差。
func spectralError(out *Clip, freqs, amps []float64) float64 {
	n := 1
	for n < len(out.Frames) {
		n <<= 1
	}
	x := make([]float64, len(out.Frames))
	for i, v := range out.Frames {
		x[i] = float64(v)
	}
	mOut := fftMag(x)
	mRef := denseRefMag(freqs, amps, out.Rate, n)
	norm := func(m []float64) float64 {
		s := 0.0
		for _, v := range m {
			if v > s {
				s = v
			}
		}
		return s
	}
	sOut, sRef := norm(mOut), norm(mRef)
	maxErr := 0.0
	for j := 0; j <= n/2; j++ {
		e := math.Abs(mOut[j]/sOut - mRef[j]/sRef)
		if e > maxErr {
			maxErr = e
		}
	}
	return maxErr
}

// fftMag 计算补零到 2 的幂长度后的前半幅度谱。
func fftMag(x []float64) []float64 {
	n := 1
	for n < len(x) {
		n <<= 1
	}
	re := make([]float64, n)
	im := make([]float64, n)
	copy(re, x)
	j := 0
	for i := 0; i < n-1; i++ {
		if i < j {
			re[i], re[j] = re[j], re[i]
			im[i], im[j] = im[j], im[i]
		}
		k := n >> 1
		for k >= 2 && k <= j {
			j -= k
			k >>= 1
		}
		j += k
	}
	for size := 2; size <= n; size <<= 1 {
		half := size >> 1
		wr := math.Cos(-2 * math.Pi / float64(size))
		wi := math.Sin(-2 * math.Pi / float64(size))
		for base := 0; base < n; base += size {
			cr, ci := 1.0, 0.0
			for k := 0; k < half; k++ {
				a, b := base+k, base+k+half
				tr := re[b]*cr - im[b]*ci
				ti := re[b]*ci + im[b]*cr
				re[b] = re[a] - tr
				im[b] = im[a] - ti
				re[a] += tr
				im[a] += ti
				cr, ci = cr*wr-ci*wi, cr*wi+ci*wr
			}
		}
	}
	mag := make([]float64, n/2+1)
	for i := range mag {
		mag[i] = math.Hypot(re[i], im[i])
	}
	return mag
}

func TestResampleUpSpectralBound(t *testing.T) {
	const src, dst, eps = 16000, 32000, 0.05
	p, err := SolveResample(src, dst, eps)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("升采样反解: %d→%d Hz, eps=%v → 核长=%d 过采样=%d β=%.3f",
		src, dst, eps, p.KernelLen, p.Oversample, p.Beta)
	c, err := NewClip(src, 8192)
	if err != nil {
		t.Fatal(err)
	}
	multiSine(c, testFreqs, testAmps)
	out, err := Resample(c, dst, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Frames) != 16384 {
		t.Fatalf("输出长度错误 %d", len(out.Frames))
	}
	if e := spectralError(out, testFreqs, testAmps); e > eps {
		t.Fatalf("升采样谱误差 %v 超过上界 %v", e, eps)
	} else {
		t.Logf("升采样谱误差 %v ≤ 上界 %v", e, eps)
	}
}

func TestResampleDownSpectralBound(t *testing.T) {
	const src, dst, eps = 16000, 8000, 0.05
	p, err := SolveResample(src, dst, eps)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("降采样反解: %d→%d Hz, eps=%v → 核长=%d 过采样=%d β=%.3f",
		src, dst, eps, p.KernelLen, p.Oversample, p.Beta)
	c, err := NewClip(src, 8192)
	if err != nil {
		t.Fatal(err)
	}
	multiSine(c, testFreqs, testAmps)
	out, err := Resample(c, dst, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Frames) != 4096 {
		t.Fatalf("输出长度错误 %d", len(out.Frames))
	}
	if e := spectralError(out, testFreqs, testAmps); e > eps {
		t.Fatalf("降采样谱误差 %v 超过上界 %v", e, eps)
	} else {
		t.Logf("降采样谱误差 %v ≤ 上界 %v", e, eps)
	}
}

func TestResampleEqualRateUnchanged(t *testing.T) {
	c, err := NewClip(16000, 500)
	if err != nil {
		t.Fatal(err)
	}
	multiSine(c, testFreqs, testAmps)
	p, err := SolveResample(16000, 16000, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Resample(c, 16000, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Frames) != len(c.Frames) {
		t.Fatalf("等采样率长度变化 %d", len(out.Frames))
	}
	for i := range out.Frames {
		if out.Frames[i] != c.Frames[i] {
			t.Fatalf("等采样率第 %d 帧被改动", i)
		}
	}
}

func TestResampleSingleSample(t *testing.T) {
	c, err := NewClip(8000, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Frames[0] = 1000
	p, err := SolveResample(8000, 16000, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Resample(c, 16000, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Frames) != 2 || out.Frames[0] != 1000 {
		t.Fatalf("单样本升采样异常 %v", out.Frames)
	}
	pd, err := SolveResample(8000, 4000, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	down, err := Resample(c, 4000, pd)
	if err != nil {
		t.Fatal(err)
	}
	if len(down.Frames) < 1 {
		t.Fatal("单样本降采样输出为空")
	}
}

func TestResampleSilence(t *testing.T) {
	c, err := NewClip(8000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	p, err := SolveResample(8000, 16000, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Resample(c, 16000, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Frames) != 2000 || Peak(out) != 0 {
		t.Fatalf("静音重采样应仍为零 长度=%d 峰值=%d", len(out.Frames), Peak(out))
	}
}

func TestResampleErrors(t *testing.T) {
	if _, err := SolveResample(0, 16000, 0.05); !errors.Is(err, ErrSampleRate) {
		t.Fatalf("源采样率为零应报 ErrSampleRate: %v", err)
	}
	if _, err := SolveResample(16000, 0, 0.05); !errors.Is(err, ErrSampleRate) {
		t.Fatalf("目标采样率为零应报 ErrSampleRate: %v", err)
	}
	if _, err := SolveResample(8000, 16000, -0.5); !errors.Is(err, ErrTarget) {
		t.Fatalf("目标误差为负应报 ErrTarget: %v", err)
	}
	c, err := NewClip(8000, 64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Resample(c, 0, ResampleParams{KernelLen: 11, Oversample: 4}); !errors.Is(err, ErrSampleRate) {
		t.Fatalf("目标采样率为零应报 ErrSampleRate: %v", err)
	}
	if _, err := Resample(c, 16000, ResampleParams{KernelLen: 10, Oversample: 4}); !errors.Is(err, ErrKernelLen) {
		t.Fatalf("核长为偶应报 ErrKernelLen: %v", err)
	}
	if _, err := Resample(c, 16000, ResampleParams{KernelLen: 11, Oversample: 0}); !errors.Is(err, ErrOversample) {
		t.Fatalf("过采样率为零应报 ErrOversample: %v", err)
	}
}
