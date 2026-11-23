package audio

import (
	"math"
	"testing"
)

// cloneClip 深拷贝素材，供两次独立运行归一使用。
func cloneClip(c *Clip) *Clip {
	f := make([]int16, len(c.Frames))
	copy(f, c.Frames)
	return &Clip{Rate: c.Rate, Frames: f}
}

// buildQuiet 轻素材：小幅正弦。
func buildQuiet() *Clip {
	c, _ := NewClip(8000, 4000)
	for i := range c.Frames {
		t := float64(i) / float64(c.Rate)
		c.Frames[i] = int16(200 * math.Sin(2*math.Pi*220*t))
	}
	return c
}

// buildLoud 响素材：近满幅正弦。
func buildLoud() *Clip {
	c, _ := NewClip(8000, 4000)
	for i := range c.Frames {
		t := float64(i) / float64(c.Rate)
		c.Frames[i] = int16(30000 * math.Sin(2*math.Pi*220*t))
	}
	return c
}

// buildVarying 忽大忽小：四个能量差异明显的分段。
func buildVarying() *Clip {
	c, _ := NewClip(8000, 4000)
	amps := []int{100, 25000, 50, 20000}
	seg := 1000
	for i := range c.Frames {
		t := float64(i) / float64(c.Rate)
		a := amps[i/seg]
		c.Frames[i] = int16(float64(a) * math.Sin(2*math.Pi*220*t))
	}
	return c
}

// buildSilence 静音素材：全零。
func buildSilence() *Clip {
	c, _ := NewClip(8000, 4000)
	return c
}

// buildSingle 单样本素材。
func buildSingle() *Clip {
	c, _ := NewClip(8000, 1)
	c.Frames[0] = 12345
	return c
}

// verifyReport 对任意报告逐一核验三条硬性保证。before 为归一前快照。
func verifyReport(t *testing.T, name string, c *Clip, before []int16, rep *NormalizeReport, cfg NormalizeConfig) {
	t.Helper()

	// 核验点 1a：归一前后峰值均不得超过满幅。
	if rep.PeakAfter > 32767 {
		t.Fatalf("[%s] 归一后峰值 %d 超过满幅", name, rep.PeakAfter)
	}
	if p := peakAbs(c.Frames); p > 32767 {
		t.Fatalf("[%s] 实际帧峰值 %d 超过满幅", name, p)
	}
	if rep.PeakBefore > 32767 {
		t.Fatalf("[%s] 归一前峰值 %d 超过满幅", name, rep.PeakBefore)
	}

	// 核验点 1b：实际限幅阈值不得超过由削顶概率反解的值。
	// 独立重算：用报告中的分段增益重建限幅前缩放幅值直方图，再独立反解阈值，
	// 与实现给出的阈值逐位比较，并复核超阈值样本占比与削顶计数。
	if rep.Threshold > 32767 {
		t.Fatalf("[%s] 阈值 %d 超过满幅", name, rep.Threshold)
	}
	hist := make([]int, 32768)
	independentlyClipped := 0
	for i, x := range before {
		var s *SegmentStat
		for k := range rep.Segments {
			if i >= rep.Segments[k].Start && i < rep.Segments[k].End {
				s = &rep.Segments[k]
				break
			}
		}
		m := int(math.Round(float64(absInt16(x)) * s.Gain))
		if m > 32767 {
			m = 32767
		}
		hist[m]++
		if m > rep.Threshold {
			independentlyClipped++
		}
	}
	independentThreshold := invertLimiterThreshold(hist, cfg.ClipProb)
	if independentThreshold != rep.Threshold {
		t.Fatalf("[%s] 独立反解阈值 %d 与实现阈值 %d 不一致",
			name, independentThreshold, rep.Threshold)
	}
	if independentlyClipped != rep.ClipCount {
		t.Fatalf("[%s] 削顶计数不一致：独立重算 %d vs 报告 %d",
			name, independentlyClipped, rep.ClipCount)
	}
	if float64(independentlyClipped)/float64(len(before)) > cfg.ClipProb+1e-12 {
		t.Fatalf("[%s] 实际削顶概率超过允许值 %.6f", name, cfg.ClipProb)
	}
	if rep.ScaledPeak > 32767 {
		t.Fatalf("[%s] 限幅前缩放峰值 %d 超过满幅", name, rep.ScaledPeak)
	}

	// 核验点 1c：响度达标——非静音素材归一后响度不得高于目标（仅允许极小数值误差）。
	if !math.IsInf(rep.LoudBefore, -1) {
		if rep.LoudAfter > cfg.TargetLUFS+0.5 {
			t.Fatalf("[%s] 归一后响度 %.2f 高于目标 %.2f 超过 0.5dB",
				name, rep.LoudAfter, cfg.TargetLUFS)
		}
	}

	// 分段增益均为正且有限。
	for _, s := range rep.Segments {
		if !(s.Gain > 0 && !math.IsInf(s.Gain, 0) && !math.IsNaN(s.Gain)) {
			t.Fatalf("[%s] 分段 %d 增益非法 %v", name, s.Index, s.Gain)
		}
		if s.PeakAfter > 32767 {
			t.Fatalf("[%s] 分段 %d 归一后峰值越界", name, s.Index)
		}
	}
}

func TestNormalizeCases(t *testing.T) {
	cfg := NormalizeConfig{TargetLUFS: -20, ClipProb: 0.01, SegmentLen: 1000}
	cases := map[string]func() *Clip{
		"轻":       buildQuiet,
		"响":       buildLoud,
		"忽大忽小": buildVarying,
		"静音":     buildSilence,
		"单样本":    buildSingle,
	}
	for name, build := range cases {
		c := build()
		before := make([]int16, len(c.Frames))
		copy(before, c.Frames)
		rep, err := Normalize(c, cfg)
		if err != nil {
			t.Fatalf("[%s] 归一失败: %v", name, err)
		}
		verifyReport(t, name, c, before, rep, cfg)

		// 核验点 2：同一素材两次运行增益相同（逐位一致），输出帧也逐位一致。
		c2 := build()
		rep2, err := Normalize(c2, cfg)
		if err != nil {
			t.Fatalf("[%s] 第二次归一失败: %v", name, err)
		}
		if len(rep.Segments) != len(rep2.Segments) {
			t.Fatalf("[%s] 两次分段数不同", name)
		}
		for i := range rep.Segments {
			if rep.Segments[i].Gain != rep2.Segments[i].Gain {
				t.Fatalf("[%s] 分段 %d 两次增益不一致: %.12f vs %.12f",
					name, i, rep.Segments[i].Gain, rep2.Segments[i].Gain)
			}
		}
		if rep.GlobalGain != rep2.GlobalGain || rep.Threshold != rep2.Threshold {
			t.Fatalf("[%s] 两次全局增益/阈值不一致", name)
		}
		for i := range c.Frames {
			if c.Frames[i] != c2.Frames[i] {
				t.Fatalf("[%s] 第 %d 帧两次输出不一致 %d vs %d",
					name, i, c.Frames[i], c2.Frames[i])
			}
		}
	}
}

func TestNormalizeVaryingSegmentGains(t *testing.T) {
	c := buildVarying()
	cfg := NormalizeConfig{TargetLUFS: -20, ClipProb: 0.01, SegmentLen: 1000}
	rep, err := Normalize(c, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Segments) != 4 {
		t.Fatalf("应得到 4 个分段，实际 %d", len(rep.Segments))
	}
	// 响段（100、25000、50、20000 中的第 2、4 段）应被压到不高于目标响度。
	for i, s := range rep.Segments {
		if !math.IsInf(s.LoudAfter, -1) && s.LoudAfter > cfg.TargetLUFS+0.5 {
			t.Fatalf("分段 %d 归一后响度 %.2f 仍高于目标", i, s.LoudAfter)
		}
	}
	// 响段使用的增益应明显小于轻段。
	if rep.Segments[1].Gain >= rep.Segments[0].Gain {
		t.Fatalf("响段增益 %.4f 应小于轻段 %.4f", rep.Segments[1].Gain, rep.Segments[0].Gain)
	}
}

func TestNormalizeSilence(t *testing.T) {
	c := buildSilence()
	rep, err := Normalize(c, NormalizeConfig{TargetLUFS: -20, ClipProb: 0.01, SegmentLen: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if !math.IsInf(rep.LoudBefore, -1) || !math.IsInf(rep.LoudAfter, -1) {
		t.Fatal("静音素材响度应为 -Inf")
	}
	if rep.PeakAfter != 0 || rep.ClipCount != 0 {
		t.Fatal("静音不应产生峰值或削顶")
	}
	// 静音段不放大噪声：增益保持 1。
	for _, s := range rep.Segments {
		if s.Gain != 1 {
			t.Fatalf("静音段增益应为 1，实际 %.4f", s.Gain)
		}
	}
	for _, x := range c.Frames {
		if x != 0 {
			t.Fatal("静音帧必须保持为零")
		}
	}
}

func TestNormalizeSingleSample(t *testing.T) {
	c := buildSingle()
	before := make([]int16, len(c.Frames))
	copy(before, c.Frames)
	cfg := NormalizeConfig{TargetLUFS: -20, ClipProb: 0.01}
	rep, err := Normalize(c, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Segments) != 1 || len(c.Frames) != 1 {
		t.Fatal("单样本素材应只有一个分段一个样本")
	}
	verifyReport(t, "单样本", c, before, rep, cfg)
	// 允许削顶概率 1% 意味着 1 个样本也不允许超过阈值，阈值必须 >= 输出幅值。
	if absInt16(c.Frames[0]) > rep.Threshold {
		t.Fatal("单样本在 1% 削顶概率下不应被削")
	}
}

func TestNormalizeParamErrors(t *testing.T) {
	// 时长为零。
	empty := &Clip{Rate: 8000, Frames: nil}
	if _, err := Normalize(empty, NormalizeConfig{TargetLUFS: -20, ClipProb: 0.01}); err == nil {
		t.Fatal("时长为零应报错")
	} else if pe, ok := err.(*ParamError); !ok || pe.Param != "frames" {
		t.Fatalf("时长为零应报参数名 frames，实际 %v", err)
	}

	// 目标响度越界（两种边界各一次）。
	for _, bad := range []float64{0, -120.1, 5} {
		c := buildQuiet()
		_, err := Normalize(c, NormalizeConfig{TargetLUFS: bad, ClipProb: 0.01})
		if err == nil {
			t.Fatalf("目标响度 %.2f 越界应报错", bad)
		} else if pe, ok := err.(*ParamError); !ok || pe.Param != "targetLUFS" {
			t.Fatalf("目标响度越界应报参数名 targetLUFS，实际 %v", err)
		}
	}

	// 采样率为零。
	c := buildQuiet()
	c.Rate = 0
	if _, err := Normalize(c, NormalizeConfig{TargetLUFS: -20, ClipProb: 0.01}); err == nil {
		t.Fatal("采样率为零应报错")
	} else if pe, ok := err.(*ParamError); !ok || pe.Param != "rate" {
		t.Fatalf("采样率为零应报参数名 rate，实际 %v", err)
	}

	// 削顶概率越界。
	for _, bad := range []float64{0, 1, -0.1, 1.5} {
		c := buildQuiet()
		_, err := Normalize(c, NormalizeConfig{TargetLUFS: -20, ClipProb: bad})
		if err == nil {
			t.Fatalf("削顶概率 %.3f 越界应报错", bad)
		} else if pe, ok := err.(*ParamError); !ok || pe.Param != "clipProb" {
			t.Fatalf("削顶概率越界应报参数名 clipProb，实际 %v", err)
		}
	}
}

func TestNormalizeInt16MinEdge(t *testing.T) {
	// -32768 是 int16 负向极值，对称满幅口径下其峰值也不得超过 32767。
	c, _ := NewClip(8000, 8)
	for i := range c.Frames {
		if i%2 == 0 {
			c.Frames[i] = -32768
		} else {
			c.Frames[i] = 32767
		}
	}
	before := make([]int16, len(c.Frames))
	copy(before, c.Frames)
	cfg := NormalizeConfig{TargetLUFS: -6, ClipProb: 0.25, SegmentLen: 4}
	rep, err := Normalize(c, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.PeakBefore > 32767 || rep.PeakAfter > 32767 || rep.ScaledPeak > 32767 {
		t.Fatalf("含 -32768 时出现越界峰值: before=%d after=%d scaled=%d",
			rep.PeakBefore, rep.PeakAfter, rep.ScaledPeak)
	}
	verifyReport(t, "int16极值", c, before, rep, cfg)
	for i, v := range c.Frames {
		if v < -32767 || v > 32767 {
			t.Fatalf("第 %d 帧输出越界 %d", i, v)
		}
	}
}

func TestApplyGainUnchanged(t *testing.T) {
	// 整体增益接口的基本行为：线性放大 + 满幅饱和，且不改变帧长。
	c, _ := NewClip(8000, 4)
	c.Frames[0], c.Frames[1], c.Frames[2], c.Frames[3] = 100, -100, 30000, -30000
	if err := ApplyGain(c, 2); err != nil {
		t.Fatal(err)
	}
	want := []int16{200, -200, 32767, -32767}
	for i := range want {
		if c.Frames[i] != want[i] {
			t.Fatalf("第 %d 帧应为 %d，实际 %d", i, want[i], c.Frames[i])
		}
	}
	if err := ApplyGain(&Clip{Rate: 8000}, 1); err == nil {
		t.Fatal("空素材应报错")
	}
}

func TestDistributionTable(t *testing.T) {
	c := buildVarying()
	rep, err := Normalize(c, NormalizeConfig{TargetLUFS: -20, ClipProb: 0.01, SegmentLen: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Distribution) != len(distEdges)+1 {
		t.Fatalf("分布表应有 %d 个区间", len(distEdges)+1)
	}
	var bSum, aSum int
	var bf, af float64
	for _, b := range rep.Distribution {
		bSum += b.BeforeCount
		aSum += b.AfterCount
		bf += b.BeforeFrac
		af += b.AfterFrac
	}
	if bSum != len(c.Frames) || aSum != len(c.Frames) {
		t.Fatal("分布表计数之和应等于总帧数")
	}
	if math.Abs(bf-1) > 1e-9 || math.Abs(af-1) > 1e-9 {
		t.Fatal("分布表占比之和应为 1")
	}
}
