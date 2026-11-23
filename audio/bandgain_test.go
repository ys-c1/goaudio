package audio

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
)

type tone struct {
	freq float64
	amp  float64
}

// mix 用若干正弦叠加填充整段采样。
func mix(c *Clip, tones []tone) {
	for i := range c.Frames {
		v := 0.0
		for _, t := range tones {
			v += t.amp * math.Sin(2*math.Pi*t.freq*float64(i)/float64(c.Rate))
		}
		c.Frames[i] = int16(math.Round(v))
	}
}

// magAt 用相关法测某个频率分量的幅度。
func magAt(c *Clip, freq float64) float64 {
	var sr, si float64
	for i, v := range c.Frames {
		ph := 2 * math.Pi * freq * float64(i) / float64(c.Rate)
		sr += float64(v) * math.Cos(ph)
		si += float64(v) * math.Sin(ph)
	}
	return 2 * math.Hypot(sr, si) / float64(len(c.Frames))
}

func dcOf(c *Clip) float64 {
	s := 0.0
	for _, v := range c.Frames {
		s += float64(v)
	}
	return s / float64(len(c.Frames))
}

// binFreq 返回恰好落在 FFT 频点上的频率，避免泄漏干扰断言。
func binFreq(rate, n, bin int) float64 {
	return float64(bin) * float64(rate) / float64(n)
}

// 低频为主：强低频 + 弱高频，自动边界应把两者分开，低频段增益只压低频。
func TestBandGainLowDominant(t *testing.T) {
	rate, n := 8000, 4096
	f1, f2 := binFreq(rate, n, 50), binFreq(rate, n, 1500) // 97.7Hz / 2929.7Hz
	c, _ := NewClip(rate, n)
	mix(c, []tone{{f1, 12000}, {f2, 500}})

	b1, b2 := magAt(c, f1), magAt(c, f2)
	rep, err := ApplyBandGain(c, BandGainConfig{Bands: 2, Gains: []float64{0.1, 1.0}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Edges[1] <= f1 || rep.Edges[1] >= f2 {
		t.Fatalf("自动边界应落在两峰之间: %v", rep.Edges)
	}
	if r := magAt(c, f1) / b1; math.Abs(r-0.1) > 0.02 {
		t.Fatalf("低频段应按增益 0.1 缩放，实际比例 %.3f", r)
	}
	if r := magAt(c, f2) / b2; math.Abs(r-1.0) > 0.05 {
		t.Fatalf("高频段应保持增益 1.0，实际比例 %.3f", r)
	}
	if rep.Energies[0] <= rep.Energies[1] {
		t.Fatalf("低频为主时低频段能量应更大: %v", rep.Energies)
	}
}

// 高频为主：弱低频 + 强高频，高频段增益只压高频。
func TestBandGainHighDominant(t *testing.T) {
	rate, n := 8000, 4096
	f1, f2 := binFreq(rate, n, 50), binFreq(rate, n, 1500)
	c, _ := NewClip(rate, n)
	mix(c, []tone{{f1, 500}, {f2, 12000}})

	b1, b2 := magAt(c, f1), magAt(c, f2)
	rep, err := ApplyBandGain(c, BandGainConfig{Bands: 2, Gains: []float64{1.0, 0.1}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Edges[1] <= f1 || rep.Edges[1] >= f2 {
		t.Fatalf("自动边界应落在两峰之间: %v", rep.Edges)
	}
	if r := magAt(c, f1) / b1; math.Abs(r-1.0) > 0.05 {
		t.Fatalf("低频段应保持增益 1.0，实际比例 %.3f", r)
	}
	if r := magAt(c, f2) / b2; math.Abs(r-0.1) > 0.02 {
		t.Fatalf("高频段应按增益 0.1 缩放，实际比例 %.3f", r)
	}
	if rep.Energies[1] <= rep.Energies[0] {
		t.Fatalf("高频为主时高频段能量应更大: %v", rep.Energies)
	}
}

// 宽带：确定性噪声覆盖整个频带，各段都应有能量，增益后输出应改变。
func TestBandGainWideband(t *testing.T) {
	rate, n := 44100, 8192
	c, _ := NewClip(rate, n)
	rng := rand.New(rand.NewSource(42))
	before := make([]int16, n)
	for i := range c.Frames {
		c.Frames[i] = int16(rng.Intn(16001) - 8000)
		before[i] = c.Frames[i]
	}
	rep, err := ApplyBandGain(c, BandGainConfig{Bands: 4, Gains: []float64{1.5, 0.5, 2.0, 0.8}})
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	for i := range c.Frames {
		if c.Frames[i] != before[i] {
			changed = true
			break
		}
	}
	if !changed {
		t.Fatal("宽带信号加分段增益后输出应改变")
	}
	for i, e := range rep.Energies {
		if e <= 0 {
			t.Fatalf("宽带信号第 %d 段能量应为正: %v", i, rep.Energies)
		}
	}
}

// 单音：边界应把唯一谱峰孤立成一段，该段增益只影响这个音。
func TestBandGainSingleTone(t *testing.T) {
	rate, n := 8000, 4096
	f := binFreq(rate, n, 200) // 390.625Hz
	c, _ := NewClip(rate, n)
	mix(c, []tone{{f, 10000}})

	b := magAt(c, f)
	rep, err := ApplyBandGain(c, BandGainConfig{Bands: 3, Gains: []float64{1.0, 2.0, 1.0}})
	if err != nil {
		t.Fatal(err)
	}
	// 谱峰应被孤立在中间段。
	if !(rep.Edges[1] < f && f < rep.Edges[2]) {
		t.Fatalf("单音应被边界孤立: f=%v edges=%v", f, rep.Edges)
	}
	if r := magAt(c, f) / b; math.Abs(r-2.0) > 0.05 {
		t.Fatalf("单音所在段应按增益 2.0 缩放，实际比例 %.3f", r)
	}
	if m := magAt(c, 2000); m > 1.0 {
		t.Fatalf("远离谱峰的频率不应被抬起: %.3f", m)
	}
}

// 静音：全零输入不得报错，输出仍全零，各段能量为零。
func TestBandGainSilence(t *testing.T) {
	c, _ := NewClip(8000, 2048)
	rep, err := ApplyBandGain(c, BandGainConfig{Bands: 3, Gains: []float64{2, 2, 2}})
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range c.Frames {
		if v != 0 {
			t.Fatalf("静音处理后第 %d 帧应为零，得到 %d", i, v)
		}
	}
	for i, e := range rep.Energies {
		if e != 0 {
			t.Fatalf("静音第 %d 段能量应为零: %v", i, rep.Energies)
		}
	}
}

// 时长不变：非 2 的幂长度走零填充路径，合成后帧数必须不变。
func TestBandGainPreservesLength(t *testing.T) {
	c, _ := NewClip(8000, 5000)
	mix(c, []tone{{300, 5000}, {1500, 3000}})
	if _, err := ApplyBandGain(c, BandGainConfig{Bands: 3, Gains: []float64{1.5, 0.5, 2.0}}); err != nil {
		t.Fatal(err)
	}
	if len(c.Frames) != 5000 {
		t.Fatalf("合成后总帧数应不变，期望 5000 得到 %d", len(c.Frames))
	}
}

// 直流不变：含直流偏置的信号处理后均值不得改变。
func TestBandGainPreservesDC(t *testing.T) {
	c, _ := NewClip(8000, 5000)
	mix(c, []tone{{300, 5000}, {1500, 3000}})
	for i := range c.Frames {
		c.Frames[i] += 2000 // 直流偏置
	}
	before := dcOf(c)
	if _, err := ApplyBandGain(c, BandGainConfig{Bands: 3, Gains: []float64{1.5, 0.5, 2.0}}); err != nil {
		t.Fatal(err)
	}
	if d := math.Abs(dcOf(c) - before); d > 1.0 {
		t.Fatalf("直流分量不得改变，处理前 %.3f 处理后 %.3f", before, dcOf(c))
	}
}

// 三类非法输入：采样率为零、段数为零、频段重叠，报错都须指名参数。
func TestBandGainErrors(t *testing.T) {
	bad := &Clip{Rate: 0, Frames: make([]int16, 64)}
	if _, err := ApplyBandGain(bad, BandGainConfig{Bands: 2, Gains: []float64{1, 1}}); err == nil || !strings.Contains(err.Error(), "Rate") {
		t.Fatalf("采样率为零应报错并指名 Rate: %v", err)
	}
	c, _ := NewClip(8000, 256)
	if _, err := ApplyBandGain(c, BandGainConfig{Bands: 0}); err == nil || !strings.Contains(err.Error(), "Bands") {
		t.Fatalf("段数为零应报错并指名 Bands: %v", err)
	}
	cfg := BandGainConfig{Bands: 3, Gains: []float64{1, 1, 1}, Edges: []float64{0, 3000, 1000, 4000}}
	if _, err := ApplyBandGain(c, cfg); err == nil || !strings.Contains(err.Error(), "Edges") {
		t.Fatalf("频段重叠应报错并指名 Edges: %v", err)
	}
}

// 两档采样率各输出一行各段能量与增益，供报告列表。
func TestBandGainReportTable(t *testing.T) {
	for _, rate := range []int{8000, 44100} {
		n := 8192
		c, _ := NewClip(rate, n)
		mix(c, []tone{{200, 8000}, {1000, 4000}, {3000, 2000}})
		rep, err := ApplyBandGain(c, BandGainConfig{Bands: 3, Gains: []float64{0.8, 1.2, 1.5}})
		if err != nil {
			t.Fatal(err)
		}
		parts := make([]string, len(rep.Energies))
		for i := range rep.Energies {
			parts[i] = fmt.Sprintf("[%.0f-%.0fHz) E=%.4g g=%.1f",
				rep.Edges[i], rep.Edges[i+1], rep.Energies[i], rep.Gains[i])
		}
		t.Logf("rate=%d  %s", rate, strings.Join(parts, "  |  "))
	}
}
