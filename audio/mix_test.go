package audio

import (
	"errors"
	"strings"
	"testing"
)

// 单路输出不变：门限内恒等通过，逐帧相等。
func TestMixBusSingleChannelIdentity(t *testing.T) {
	c, _ := NewClip(8000, 1024)
	if err := Sine(c, 440, 20000); err != nil {
		t.Fatal(err)
	}
	out, err := MixBus([]*Clip{c}, MixConfig{Weights: []float64{1}, Threshold: 32767})
	if err != nil {
		t.Fatal(err)
	}
	if out.Rate != c.Rate || len(out.Frames) != len(c.Frames) {
		t.Fatalf("尺寸改变 %d %d", out.Rate, len(out.Frames))
	}
	for i := range c.Frames {
		if out.Frames[i] != c.Frames[i] {
			t.Fatalf("单路输出第 %d 帧被改动: %d -> %d", i, c.Frames[i], out.Frames[i])
		}
	}
}

// 逐类参数错误：通道数为零、权重为负、采样率不一致，错误信息须带参数名。
func TestMixBusParamErrors(t *testing.T) {
	a, _ := NewClip(8000, 64)
	b, _ := NewClip(8000, 64)
	other, _ := NewClip(16000, 64)

	// 通道数为零
	_, err := MixBus(nil, MixConfig{Weights: nil, Threshold: 32767})
	if err == nil || !errors.Is(err, ErrNoChannel) ||
		!strings.Contains(err.Error(), "channels") {
		t.Fatalf("通道数为零应报参数 channels，got %v", err)
	}

	// 权重数量不匹配
	_, err = MixBus([]*Clip{a, b}, MixConfig{Weights: []float64{1}, Threshold: 32767})
	if err == nil || !strings.Contains(err.Error(), "weights") {
		t.Fatalf("权重数量错误应报参数 weights，got %v", err)
	}

	// 权重为负
	_, err = MixBus([]*Clip{a, b}, MixConfig{Weights: []float64{1, -0.5}, Threshold: 32767})
	if err == nil || !strings.Contains(err.Error(), "weights[1]") {
		t.Fatalf("负权重应报参数 weights[1]，got %v", err)
	}

	// 采样率不一致
	_, err = MixBus([]*Clip{a, other}, MixConfig{Weights: []float64{1, 1}, Threshold: 32767})
	if err == nil || !strings.Contains(err.Error(), "clips[1].Rate") {
		t.Fatalf("采样率不一致应报参数 clips[1].Rate，got %v", err)
	}

	// 帧数不一致（同类逐路检查）
	short, _ := NewClip(8000, 32)
	_, err = MixBus([]*Clip{a, short}, MixConfig{Weights: []float64{1, 1}, Threshold: 32767})
	if err == nil || !strings.Contains(err.Error(), "clips[1].Frames") {
		t.Fatalf("帧数不一致应报参数 clips[1].Frames，got %v", err)
	}

	// 门限非法
	_, err = MixBus([]*Clip{a}, MixConfig{Weights: []float64{1}, Threshold: 0})
	if err == nil || !strings.Contains(err.Error(), "threshold") {
		t.Fatalf("门限非法应报参数 threshold，got %v", err)
	}
}

// 能量守恒（限幅前）：未触发限幅的场景，限幅前后能量差必须严格为零。
func TestMixBusEnergyConservationBeforeLimiter(t *testing.T) {
	rate, frames := 8000, 8000

	// 两路同相、幅度各 5000：加权和峰值 10000 < 门限，限幅器不介入。
	c1, _ := NewClip(rate, frames)
	c2, _ := NewClip(rate, frames)
	if err := Sine(c1, 100, 5000); err != nil {
		t.Fatal(err)
	}
	if err := Sine(c2, 100, 5000); err != nil {
		t.Fatal(err)
	}
	clips := []*Clip{c1, c2}
	weights := []float64{1, 1}

	pre, err := NaiveMix(clips, weights) // 限幅前检查点
	if err != nil {
		t.Fatal(err)
	}
	post, err := MixBus(clips, MixConfig{Weights: weights, Threshold: 32767})
	if err != nil {
		t.Fatal(err)
	}
	ePre, ePost := Energy(pre), Energy(post)
	if ePre-ePost != 0 {
		t.Fatalf("未触发限幅时能量差必须为零: E_pre=%.1f E_post=%.1f delta=%v",
			ePre, ePost, ePre-ePost)
	}

	// 反相完全相消：两路互相抵消，能量为零，差值同样为零。
	neg, _ := NewClip(rate, frames)
	for i := range c1.Frames {
		neg.Frames[i] = -c1.Frames[i]
	}
	pre2, _ := NaiveMix([]*Clip{c1, neg}, []float64{1, 1})
	post2, _ := MixBus([]*Clip{c1, neg}, MixConfig{Weights: []float64{1, 1}, Threshold: 32767})
	if e2pre, e2post := Energy(pre2), Energy(post2); e2pre != 0 || e2pre-e2post != 0 {
		t.Fatalf("反相相消能量应为零且守恒: E_pre=%.1f E_post=%.1f", e2pre, e2post)
	}
}

// 软限幅生效：和信号超出 int16 时软限幅不削顶、不溢出，峰值受软拐点压制。
func TestMixBusSoftLimitEngages(t *testing.T) {
	rate, frames := 8000, 8000
	clips := make([]*Clip, 4)
	for i := range clips {
		c, _ := NewClip(rate, frames)
		if err := Sine(c, 100, 30000); err != nil {
			t.Fatal(err)
		}
		clips[i] = c
	}
	weights := []float64{1, 1, 1, 1}

	naive, err := NaiveMix(clips, weights) // 旧行为：硬削顶到 32767
	if err != nil {
		t.Fatal(err)
	}
	// 门限 24000：和信号峰值 120000 远超门限，走指数软膝，输出不削顶。
	out, err := MixBus(clips, MixConfig{Weights: weights, Threshold: 24000})
	if err != nil {
		t.Fatal(err)
	}
	if p := Peak(out); p > 32767 {
		t.Fatalf("输出溢出 int16: peak=%d", p)
	}
	// 软膝渐近天花板 32767 但严格达不到，因此峰值严格小于硬削顶的饱和值。
	if Peak(out) >= Peak(naive) {
		t.Fatalf("软限幅峰值应小于硬削顶峰值: soft=%d hard=%d", Peak(out), Peak(naive))
	}
	// 触发限幅后能量被有意削减，此时能量差不为零（守恒只保证到限幅前）。
	if Energy(naive)-Energy(out) <= 0 {
		t.Fatal("限幅介入后输出能量应低于限幅前")
	}
}

// 软限幅数学性质：门限处连续，门限内恒等，输出有界且严格不削顶。
func TestSoftLimitShape(t *testing.T) {
	if SoftLimit(100, 1000) != 100 {
		t.Fatal("门限内应恒等返回")
	}
	if SoftLimit(-100, 1000) != -100 {
		t.Fatal("负向门限内应恒等返回")
	}
	// 门限紧邻处只做轻微压缩，不发生硬跳变。
	if got := SoftLimit(1001, 1000); got <= 1000 || got >= 1001 {
		t.Fatalf("门限紧邻处应平滑过渡，got %v", got)
	}
	// 超门限被压回 (T, C) 区间内：s=2000,T=1000 -> y≈1632。
	if got := SoftLimit(2000, 1000); got <= 1000 || got >= 32767 {
		t.Fatalf("软膝应落在 (T, C) 区间，got %v", got)
	}
	// 有限大输入严格不触顶（不削顶），大输入充分逼近天花板 C=32767。
	if got := SoftLimit(1e6, 1000); got >= 32767 || got < 32766 {
		t.Fatalf("应逼近天花板且严格小于 32767，got %v", got)
	}
	if got := SoftLimit(-1e6, 1000); got <= -32767 || got > -32766 {
		t.Fatalf("负向应逼近 -C 且严格大于 -32767，got %v", got)
	}
	// 门限开满时在门限处平滑饱和。
	if got := SoftLimit(40000, 32767); got != 32767 {
		t.Fatalf("门限开满时应饱和到门限，got %v", got)
	}
	// 门限处连续。
	if got := SoftLimit(1000, 1000); got != 1000 {
		t.Fatalf("门限处应连续取值 T，got %v", got)
	}
}
