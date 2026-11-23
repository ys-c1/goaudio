package audio

import (
	"math"
	"testing"
)

// 反解关系：L = round(τ·rate)，α^(L-1) = level
func TestReverbSolve(t *testing.T) {
	r, err := NewReverb(8000, 0.5, 0.001, true) // -60dB 尾电平
	if err != nil {
		t.Fatal(err)
	}
	if r.TailSamples != 4000 {
		t.Fatalf("尾音长度反解错误: got %d want 4000", r.TailSamples)
	}
	got := math.Pow(r.Alpha, float64(r.TailSamples-1))
	if math.Abs(got-0.001) > 1e-12 {
		t.Fatalf("衰减系数反解不满足 α^(L-1)=level: got %.6f", got)
	}
	if ke := r.KernelEnergy(); math.Abs(ke-1.0) > 1e-12 {
		t.Fatalf("卷积核单位能量不变量失败: %.12f", ke)
	}
	if r.TailDuration().Seconds() != 0.5 {
		t.Fatalf("尾音物理时长错误: %v", r.TailDuration())
	}
}

// 三类非法参数必须报出参数名
func TestReverbParamErrors(t *testing.T) {
	if _, err := NewReverb(8000, 0, 0.001, true); err != ErrDecayTimeZero {
		t.Fatalf("时长为零应返回 ErrDecayTimeZero, got %v", err)
	}
	if _, err := NewReverb(8000, -1, 0.001, true); err != ErrDecayTimeZero {
		t.Fatalf("时长为负应返回 ErrDecayTimeZero, got %v", err)
	}
	if _, err := NewReverb(8000, 0.5, 0, true); err != ErrDecayZero {
		t.Fatalf("衰减为零应返回 ErrDecayZero, got %v", err)
	}
	if _, err := NewReverb(8000, 0.5, 1.5, true); err != ErrDecayLevel {
		t.Fatalf("电平越界应返回 ErrDecayLevel, got %v", err)
	}

	r, err := NewReverb(8000, 0.5, 0.001, true)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := NewClip(44100, 64) // 采样率与混响器不一致
	if _, err := r.Process(c); err != ErrRateMismatch {
		t.Fatalf("采样率不一致应返回 ErrRateMismatch, got %v", err)
	}
	if _, err := r.ProcessFloat(c); err != ErrRateMismatch {
		t.Fatalf("浮点路径采样率不一致应返回 ErrRateMismatch, got %v", err)
	}
}

// 未启用：逐样本输出不变，长度不变
func TestReverbDisabledPassthrough(t *testing.T) {
	r, err := NewReverb(8000, 0.5, 0.001, false)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := NewClip(8000, 137)
	if err := Sine(c, 440, 12000); err != nil {
		t.Fatal(err)
	}
	out, err := r.Process(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Frames) != len(c.Frames) {
		t.Fatalf("未启用时长度必须不变: %d vs %d", len(out.Frames), len(c.Frames))
	}
	for i := range c.Frames {
		if out.Frames[i] != c.Frames[i] {
			t.Fatalf("第 %d 帧被改动: %d vs %d", i, out.Frames[i], c.Frames[i])
		}
	}
	fout, err := r.ProcessFloat(c)
	if err != nil || len(fout) != len(c.Frames) {
		t.Fatalf("浮点直通路径错误: %v len=%d", err, len(fout))
	}
}

// 脉冲输入：输出长度 = N+L-1，浮点能量比严格为 1
func TestReverbImpulseInvariant(t *testing.T) {
	r, _ := NewReverb(8000, 0.25, 0.001, true)
	c, _ := NewClip(8000, 1)
	c.Frames[0] = 10000

	out, err := r.Process(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Frames) != r.TailSamples {
		t.Fatalf("全卷积长度错误: got %d want %d", len(out.Frames), r.TailSamples)
	}

	inF := FramesToFloat(c.Frames)
	yF, err := r.ProcessFloat(c)
	if err != nil {
		t.Fatal(err)
	}
	ratio := Energy(yF) / Energy(inF)
	if math.Abs(ratio-1.0) > 1e-12 {
		t.Fatalf("脉冲能量守恒不变量失败: 前后能量比 = %.12f", ratio)
	}
	// 输出形状应与归一化核成正比
	if math.Abs(yF[0]-10000*r.Kernel[0]) > 1e-6 {
		t.Fatalf("脉冲响应形状错误: %.6f vs %.6f", yF[0], 10000*r.Kernel[0])
	}
}

// FFT 卷积与朴素卷积必须数值一致（五类用例统一过一遍）
func TestFFTVsNaive(t *testing.T) {
	r, _ := NewReverb(8000, 0.1, 0.01, true)
	cases := map[string][]int16{
		"脉冲":   {1, 0, 0, 0, 0, 0, 0, 0},
		"正弦":   sineFrames(8, 2, 1000),
		"静音":   make([]int16, 16),
		"满幅":   fillFrames(16, 32767),
		"极短片段": {32767, -32768},
	}
	for name, frames := range cases {
		x := FramesToFloat(frames)
		naive := ConvolveNaive(x, r.Kernel)
		fast := ConvolveFFT(x, r.Kernel)
		if len(naive) != len(fast) {
			t.Fatalf("%s: 长度不一致 %d vs %d", name, len(naive), len(fast))
		}
		for i := range naive {
			if math.Abs(naive[i]-fast[i]) > 1e-8*math.Max(1, math.Abs(naive[i])) {
				t.Fatalf("%s: 第 %d 点 FFT 与朴素卷积不一致 %v vs %v", name, i, fast[i], naive[i])
			}
		}
	}
}

// 五类用例的端到端行为：长度、静音保持、满幅钳位、极短片段
func TestReverbAllCases(t *testing.T) {
	r, _ := NewReverb(8000, 0.05, 0.01, true)

	// 静音卷积后仍为静音
	silence := &Clip{Rate: 8000, Frames: make([]int16, 100)}
	out, err := r.Process(silence)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range out.Frames {
		if v != 0 {
			t.Fatalf("静音用例第 %d 帧非零: %d", i, v)
		}
	}

	// 满幅：卷积峰值受归一化增益约束，不允许出现 32767/-32768 之外的值
	full := &Clip{Rate: 8000, Frames: fillFrames(500, 32767)}
	out, err = r.Process(full)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Frames) != 500+r.TailSamples-1 {
		t.Fatalf("满幅用例全卷积长度错误: %d", len(out.Frames))
	}
	peak := Peak(out)
	if peak > 32767 {
		t.Fatalf("满幅用例出现越界峰值 %d", peak)
	}

	// 极短片段（2 帧）也必须完整补尾
	tiny := &Clip{Rate: 8000, Frames: []int16{32767, -32768}}
	out, err = r.Process(tiny)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Frames) != 2+r.TailSamples-1 {
		t.Fatalf("极短片段长度错误: %d", len(out.Frames))
	}
	if out.Frames[0] != clampInt16(32767*r.Kernel[0]) {
		t.Fatalf("极短片段首帧错误: %d", out.Frames[0])
	}
}

func sineFrames(n int, cycles float64, amp float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(amp * math.Sin(2*math.Pi*cycles*float64(i)/float64(n)))
	}
	return out
}

func fillFrames(n int, v int16) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = v
	}
	return out
}
