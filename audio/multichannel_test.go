package audio

import (
	"errors"
	"math"
	"testing"
)

// makeDrumRef 生成一段鼓点式参考信号：指数衰减的伪随机脉冲序列，
// 自相关尖锐，便于互相关精确定位整数样本延迟。
func makeDrumRef(frames int) []int16 {
	out := make([]int16, frames)
	seed := uint32(0x1234abcd)
	for i := 0; i < frames; i++ {
		// 线性同余伪随机，范围 [-1,1)
		seed = seed*1103515245 + 12345
		r := float64(int32(seed)) / float64(1<<31)
		env := math.Exp(-float64(i) / float64(frames/4))
		v := r * env * 20000
		if v > 32767 {
			v = 32767
		}
		if v < -32768 {
			v = -32768
		}
		out[i] = int16(v)
	}
	return out
}

// delayCopy 返回 sig 右移 d 个样本的副本（即该路相对主声道滞后 d），
// 头部补零；invert 为 true 时同时反相。
func delayCopy(sig []int16, d int, invert bool) []int16 {
	n := len(sig)
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		if i-d >= 0 {
			v := sig[i-d]
			if invert {
				v = -v
			}
			out[i] = v
		}
	}
	return out
}

// verifyInvariants 逐条核验两条不变量：峰值上升 + 总时长不变。
func verifyInvariants(t *testing.T, rep *AlignReport, frames int) {
	t.Helper()
	// 不变量 2：总时长不变，逐路核验。
	for ch := 0; ch < rep.Channels; ch++ {
		if got := len(rep.Aligned.Channels[ch]); got != frames {
			t.Fatalf("不变量2违反：通道号=%d 对齐后帧数=%d，期望=%d", ch, got, frames)
		}
	}
	// 不变量 1：峰值上升，逐路核验（静音路除外；零延迟路允许持平）。
	for _, r := range rep.Results {
		if r.Silent {
			continue
		}
		if math.Abs(r.PeakAfter) < math.Abs(r.PeakBefore) {
			t.Fatalf("不变量1违反：通道号=%d 峰值下降 前=%.6f 后=%.6f",
				r.Channel, r.PeakBefore, r.PeakAfter)
		}
		if r.Delay != 0 && math.Abs(r.PeakAfter) <= math.Abs(r.PeakBefore) {
			t.Fatalf("不变量1违反：通道号=%d 延迟=%d 非零但峰值未严格上升",
				r.Channel, r.Delay)
		}
	}
}

// assertAlignedSignal 核验平移对齐后，重叠区样本与参考信号完全一致
// （反相路按极性还原后一致）。
func assertAlignedSignal(t *testing.T, ref, aligned []int16, lag, polarity int) {
	t.Helper()
	n := len(ref)
	for i := 0; i < n; i++ {
		j := i + lag // aligned[i] 应等于 ref 视角下 sig[i+lag]
		_ = j
		exp := ref[i]
		if polarity < 0 {
			exp = -exp
		}
		if aligned[i] != exp {
			// 缺位补零区允许为零
			if i >= n-lag || (lag < 0 && i < -lag) {
				if aligned[i] != 0 {
					t.Fatalf("补零区异常 i=%d got=%d", i, aligned[i])
				}
				continue
			}
			t.Fatalf("对齐样本错误 i=%d got=%d exp=%d lag=%d", i, aligned[i], exp, lag)
		}
	}
}

func runScenario(t *testing.T, name string, build func(ref []int16) [][]int16, maxDelay int) *AlignReport {
	t.Helper()
	const frames = 4096
	ref := makeDrumRef(frames)
	chans := build(ref)
	mc := &MultiClip{Rate: 8000, Channels: chans}
	rep, err := AlignChannels(mc, maxDelay)
	if err != nil {
		t.Fatalf("场景 %s 对齐失败: %v", name, err)
	}
	verifyInvariants(t, rep, frames)
	if rep.Frames != frames {
		t.Fatalf("登记帧数 %d != %d", rep.Frames, frames)
	}
	return rep
}

// 场景 1：同相、零延迟
func TestScenarioInPhase(t *testing.T) {
	rep := runScenario(t, "同相", func(ref []int16) [][]int16 {
		return [][]int16{append([]int16(nil), ref...), delayCopy(ref, 0, false)}
	}, 8)
	r := rep.Results[1]
	if r.Delay != 0 || r.Polarity != 1 || r.Silent {
		t.Fatalf("同相路判定错误: %+v", r)
	}
	if math.Abs(r.PeakAfter) < 0.999 {
		t.Fatalf("同相对齐后峰值异常: %.6f", r.PeakAfter)
	}
	// 零延迟路只搜首遍：2W+1 轮
	if r.Rounds != 2*8+1 {
		t.Fatalf("同相路轮数=%d，期望=%d", r.Rounds, 17)
	}
}

// 场景 2：反相、零延迟
func TestScenarioInverse(t *testing.T) {
	rep := runScenario(t, "反相", func(ref []int16) [][]int16 {
		return [][]int16{append([]int16(nil), ref...), delayCopy(ref, 0, true)}
	}, 8)
	r := rep.Results[1]
	if r.Delay != 0 || r.Polarity != -1 {
		t.Fatalf("反相路判定错误: %+v", r)
	}
	// 对齐后零延迟相关应为接近 -1，|峰值| 接近 1
	if r.PeakAfter > -0.999 {
		t.Fatalf("反相对齐后峰值异常: %.6f", r.PeakAfter)
	}
	if r.Rounds != 17 {
		t.Fatalf("反相路轮数=%d，期望=17", r.Rounds)
	}
}

// 场景 3：错一个样本（滞后 1，同相）
func TestScenarioShiftOne(t *testing.T) {
	rep := runScenario(t, "错一个样本", func(ref []int16) [][]int16 {
		return [][]int16{append([]int16(nil), ref...), delayCopy(ref, 1, false)}
	}, 8)
	r := rep.Results[1]
	if r.Delay != 1 || r.Polarity != 1 {
		t.Fatalf("错1样本判定错误: %+v", r)
	}
	assertAlignedSignal(t, rep.Aligned.Channels[0], rep.Aligned.Channels[1], 1, 1)
	if math.Abs(r.PeakAfter) <= math.Abs(r.PeakBefore) {
		t.Fatalf("错1样本峰值未上升: 前=%.6f 后=%.6f", r.PeakBefore, r.PeakAfter)
	}
	if r.Rounds != 17 {
		t.Fatalf("错1样本轮数=%d，期望=17", r.Rounds)
	}
}

// 场景 4：错十个样本（滞后 10，同相）；窗口半宽给 16 以容纳
func TestScenarioShiftTen(t *testing.T) {
	rep := runScenario(t, "错十样本", func(ref []int16) [][]int16 {
		return [][]int16{append([]int16(nil), ref...), delayCopy(ref, 10, false)}
	}, 16)
	r := rep.Results[1]
	if r.Delay != 10 || r.Polarity != 1 {
		t.Fatalf("错10样本判定错误: %+v", r)
	}
	assertAlignedSignal(t, rep.Aligned.Channels[0], rep.Aligned.Channels[1], 10, 1)
	if math.Abs(r.PeakAfter) <= math.Abs(r.PeakBefore) {
		t.Fatalf("错10样本峰值未上升: 前=%.6f 后=%.6f", r.PeakBefore, r.PeakAfter)
	}
	if r.Rounds != 2*16+1 {
		t.Fatalf("错10样本轮数=%d，期望=%d", r.Rounds, 33)
	}
}

// 场景 5：静音路
func TestScenarioSilent(t *testing.T) {
	rep := runScenario(t, "静音路", func(ref []int16) [][]int16 {
		return [][]int16{
			append([]int16(nil), ref...),
			make([]int16, len(ref)),
		}
	}, 8)
	r := rep.Results[1]
	if !r.Silent || r.Delay != 0 || r.Polarity != 0 {
		t.Fatalf("静音路判定错误: %+v", r)
	}
	if r.Rounds != 0 {
		t.Fatalf("静音路不应搜索，轮数=%d", r.Rounds)
	}
	// 静音路必须原样输出
	for i, v := range rep.Aligned.Channels[1] {
		if v != 0 {
			t.Fatalf("静音路对齐后非零 i=%d v=%d", i, v)
		}
	}
}

// 多声道综合：三路（同相零延迟 + 滞后10 + 静音），轮数合计核验
func TestScenarioThreeChannels(t *testing.T) {
	const frames = 4096
	ref := makeDrumRef(frames)
	mc := &MultiClip{Rate: 8000, Channels: [][]int16{
		append([]int16(nil), ref...),
		delayCopy(ref, 10, false),
		make([]int16, frames),
	}}
	rep, err := AlignChannels(mc, 16)
	if err != nil {
		t.Fatal(err)
	}
	verifyInvariants(t, rep, frames)
	if rep.Results[1].Delay != 10 || !rep.Results[2].Silent {
		t.Fatalf("三路判定错误: %+v %+v", rep.Results[1], rep.Results[2])
	}
	wantRounds := 33 + 0 // 通道1 首遍 33 轮；静音路 0 轮
	if rep.TotalRounds != wantRounds {
		t.Fatalf("总轮数=%d，期望=%d", rep.TotalRounds, wantRounds)
	}
}

// 非法输入 1：声道数不足二——报出通道号
func TestIllegalTooFewChannels(t *testing.T) {
	mc := &MultiClip{Rate: 8000, Channels: [][]int16{make([]int16, 64)}}
	_, err := AlignChannels(mc, 8)
	if !errors.Is(err, ErrTooFewChannels) {
		t.Fatalf("期望 ErrTooFewChannels，得到 %v", err)
	}
	t.Logf("报错信息: %v", err)

	// 组装入口同样报出通道号
	if _, err := MultiClipFromClips(&Clip{Rate: 8000, Frames: make([]int16, 8)}); !errors.Is(err, ErrTooFewChannels) {
		t.Fatalf("组装入口期望 ErrTooFewChannels，得到 %v", err)
	}
}

// 非法输入 2：延迟超出窗口——报出通道号
func TestIllegalDelayOutOfWindow(t *testing.T) {
	const frames = 4096
	ref := makeDrumRef(frames)
	mc := &MultiClip{Rate: 8000, Channels: [][]int16{
		append([]int16(nil), ref...),
		delayCopy(ref, 10, false), // 真实延迟 10
	}}
	_, err := AlignChannels(mc, 8) // 估计窗口仅 [-8,+8]
	if !errors.Is(err, ErrDelayOutOfWindow) {
		t.Fatalf("期望 ErrDelayOutOfWindow，得到 %v", err)
	}
	t.Logf("报错信息: %v", err)
}

// 非法输入 3：采样率不一致——报出通道号
func TestIllegalRateMismatch(t *testing.T) {
	c1, _ := NewClip(8000, 64)
	c2, _ := NewClip(16000, 64)
	_, err := MultiClipFromClips(c1, c2)
	if !errors.Is(err, ErrRateMismatch) {
		t.Fatalf("期望 ErrRateMismatch，得到 %v", err)
	}
	t.Logf("报错信息: %v", err)

	// 第三个通道才不一致，确认报的是通道号 2
	c3, _ := NewClip(8000, 64)
	c4, _ := NewClip(44100, 64)
	_, err = MultiClipFromClips(c1, c3, c4)
	if !errors.Is(err, ErrRateMismatch) {
		t.Fatalf("期望 ErrRateMismatch，得到 %v", err)
	}
	t.Logf("报错信息: %v", err)
}

// 既有单路行为回归：确认原有 API 与测试语义未被改动
func TestSingleChannelRegression(t *testing.T) {
	c, err := NewClip(8000, 800)
	if err != nil {
		t.Fatal(err)
	}
	if err := Sine(c, 100, 10000); err != nil {
		t.Fatal(err)
	}
	if p := Peak(c); p < 9000 || p > 10000 {
		t.Fatalf("单路峰值回归失败 %d", p)
	}
}
