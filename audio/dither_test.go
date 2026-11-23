package audio

import (
	"math"
	"testing"
)

const testSeed = uint64(20261124)

// makeCase 构造五类用例：极轻、正常、满幅、静音、单样本。
func makeCases(t *testing.T) map[string]*Clip {
	t.Helper()
	cases := map[string]*Clip{}

	// 极轻：幅度 2（8-bit 下 Δ=256，无抖动必然整段塌成静音）
	light, err := NewClip(8000, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := Sine(light, 100, 2); err != nil {
		t.Fatal(err)
	}
	cases["极轻"] = light

	// 正常：幅度 8000
	normal, err := NewClip(8000, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := Sine(normal, 100, 8000); err != nil {
		t.Fatal(err)
	}
	cases["正常"] = normal

	// 满幅：幅度 32767
	full, err := NewClip(8000, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := Sine(full, 100, 32767); err != nil {
		t.Fatal(err)
	}
	cases["满幅"] = full

	// 静音：全零
	silent, err := NewClip(8000, 4096)
	if err != nil {
		t.Fatal(err)
	}
	cases["静音"] = silent

	// 单样本
	one, err := NewClip(8000, 1)
	if err != nil {
		t.Fatal(err)
	}
	one.Frames[0] = 1
	cases["单样本"] = one

	return cases
}

// TestDitherInvariants 对每个用例逐条核验两条不变量：
//  1. 量化前后静音段比例不得上升（死区口径，与量化器判零同口径；
//     并附无抖动基线对照）；
//  2. 同一种子两次运行逐样本相同。
func TestDitherInvariants(t *testing.T) {
	const bitDepth = 8
	delta, err := QuantStep(bitDepth)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range makeCases(t) {
		t.Run(name, func(t *testing.T) {
			out1, err := DitherQuantize(c, bitDepth, testSeed, 0)
			if err != nil {
				t.Fatal(err)
			}
			out2, err := DitherQuantize(c, bitDepth, testSeed, 0)
			if err != nil {
				t.Fatal(err)
			}
			plain, err := Quantize(c, bitDepth)
			if err != nil {
				t.Fatal(err)
			}

			// 不变量 2：同种子逐样本复现
			for i := range out1 {
				if out1[i] != out2[i] {
					t.Fatalf("不变量2失败：第 %d 帧两次运行不一致 %d != %d", i, out1[i], out2[i])
				}
			}

			// 不变量 1a：死区静音比例相对输入不上升
			inSilence := DeadbandSilenceRatio(c.Frames, delta)
			outSilence := DeadbandSilenceRatio(out1, delta)
			if outSilence > inSilence {
				t.Fatalf("不变量1a失败：死区静音比例上升 %.6f -> %.6f", inSilence, outSilence)
			}

			// 不变量 1b：死区静音比例相对无抖动基线不上升
			plainSilence := DeadbandSilenceRatio(plain, delta)
			if outSilence > plainSilence {
				t.Fatalf("不变量1b失败：抖动后静音比例高于无抖动基线 %.6f -> %.6f", plainSilence, outSilence)
			}

			t.Logf("用例=%-6s 输入死区静音=%.4f 无抖动=%.4f 抖动后=%.4f 输入零样本=%.4f 输出零样本=%.4f",
				name, inSilence, plainSilence, outSilence,
				SilenceRatio(c.Frames), SilenceRatio(out1))
		})
	}
}

// TestLightSignalCollapseFixed 直接复现需求描述的缺陷并验证修复：
// 极轻信号无抖动整段塌成静音，加抖动后不再是整段静音。
func TestLightSignalCollapseFixed(t *testing.T) {
	c := makeCases(t)["极轻"]
	plain, err := Quantize(c, 8)
	if err != nil {
		t.Fatal(err)
	}
	if SilenceRatio(plain) != 1.0 {
		t.Fatalf("基线前提不成立：无抖动应整段静音，实际零样本比例 %.4f", SilenceRatio(plain))
	}
	out, err := DitherQuantize(c, 8, testSeed, 0)
	if err != nil {
		t.Fatal(err)
	}
	if SilenceRatio(out) >= 1.0 {
		t.Fatal("抖动后仍整段静音，缺陷未修复")
	}
}

// TestAmplitudeNotAboveTarget 按目标 SNR 推算抖动幅度，
// 实测幅度（真正施加给样本的抖动序列 RMS）高于推算值即按未完成计（失败）。
func TestAmplitudeNotAboveTarget(t *testing.T) {
	for _, bitDepth := range []int{4, 8, 12, 16} {
		delta, err := QuantStep(bitDepth)
		if err != nil {
			t.Fatal(err)
		}
		native, err := NativeTPDFSNR(bitDepth)
		if err != nil {
			t.Fatal(err)
		}
		target, err := TargetDitherRMS(bitDepth, native)
		if err != nil {
			t.Fatal(err)
		}
		const n = 16384
		dither, gotDelta, err := CalibratedDither(n, bitDepth, testSeed, native)
		if err != nil {
			t.Fatal(err)
		}
		var sumSq float64
		for _, v := range dither {
			sumSq += v * v
		}
		got := math.Sqrt(sumSq / n)
		if got > target {
			t.Fatalf("位深 %d：实测抖动幅度 %.6f 高于推算值 %.6f，按未完成计", bitDepth, got, target)
		}
		// 标定后应与推算值非常接近（0.1% 安全余量），防止幅度被静默压没
		if got < target*0.99 {
			t.Fatalf("位深 %d：实测幅度 %.6f 远低于推算值 %.6f，标定异常", bitDepth, got, target)
		}
		// 取固有 SNR 时正推 Δ/√6 与反推必须自洽
		wantFull := delta / math.Sqrt(6)
		if math.Abs(target-wantFull) > 1e-6*wantFull {
			t.Fatalf("位深 %d：SNR 反推 %.6f 与 Δ/√6 %.6f 不自洽", bitDepth, target, wantFull)
		}
		t.Logf("位深=%2d Δ=%7.1f 目标SNR=%7.3fdB 推算RMS=%.6f 实测RMS=%.6f 比值=%.6f",
			bitDepth, gotDelta, native, target, got, got/target)
	}
}

// TestFirstSixteenDither 打印抖动序列前 16 项，并核验可复现。
func TestFirstSixteenDither(t *testing.T) {
	seq1, err := DitherSequence(16, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	seq2, err := DitherSequence(16, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("抖动序列前 16 项（种子 %d，TPDF 归一化到 [-1,1)）：", testSeed)
	for i, v := range seq1 {
		if seq1[i] != seq2[i] {
			t.Fatalf("第 %d 项不可复现", i)
		}
		t.Logf("  d[%2d] = %+.10f", i, v)
	}
}

// TestDitherParameterErrors 三类参数越界各报出参数名。
func TestDitherParameterErrors(t *testing.T) {
	c, err := NewClip(8000, 16)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DitherQuantize(c, 0, testSeed, 0); err != ErrBitDepth {
		t.Fatalf("位深越界应报 ErrBitDepth，实际 %v", err)
	}
	if _, err := DitherQuantize(c, 17, testSeed, 0); err != ErrBitDepth {
		t.Fatalf("位深越界应报 ErrBitDepth，实际 %v", err)
	}
	if _, err := DitherQuantize(c, 8, 0, 0); err != ErrSeed {
		t.Fatalf("种子为零应报 ErrSeed，实际 %v", err)
	}
	badRate := &Clip{Rate: 0, Frames: make([]int16, 16)}
	if _, err := DitherQuantize(badRate, 8, testSeed, 0); err != ErrSampleRate {
		t.Fatalf("采样率为零应报 ErrSampleRate，实际 %v", err)
	}
	if _, err := DitherQuantize(c, 8, testSeed, -3); err != ErrTargetSNR {
		t.Fatalf("目标 SNR 非正应报 ErrTargetSNR，实际 %v", err)
	}
	if !contains(ErrBitDepth.Error(), "bitDepth") ||
		!contains(ErrSeed.Error(), "seed") ||
		!contains(ErrSampleRate.Error(), "rate") {
		t.Fatal("错误信息必须带参数名")
	}
}

// TestPassthroughUnchanged 原有直通路径保持不变：
// 16-bit 无抖动量化逐样本恒等；原有合成/包络/WAV 行为由既有测试覆盖。
func TestPassthroughUnchanged(t *testing.T) {
	c, err := NewClip(8000, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := Sine(c, 440, 12345); err != nil {
		t.Fatal(err)
	}
	out, err := Quantize(c, 16)
	if err != nil {
		t.Fatal(err)
	}
	for i := range c.Frames {
		if out[i] != c.Frames[i] {
			t.Fatalf("16-bit 直通路径被改变：第 %d 帧 %d -> %d", i, c.Frames[i], out[i])
		}
	}
	// DitherQuantize 不修改输入
	before := append([]int16(nil), c.Frames...)
	if _, err := DitherQuantize(c, 8, testSeed, 0); err != nil {
		t.Fatal(err)
	}
	for i := range c.Frames {
		if c.Frames[i] != before[i] {
			t.Fatal("抖动量化修改了输入 clip")
		}
	}
}

// TestTPDFShape 核验固定种子序列的统计形状：均值近零、RMS 接近 1/sqrt(6)、
// 支撑域落在 [-1,1)。
func TestTPDFShape(t *testing.T) {
	seq, err := DitherSequence(1<<16, testSeed)
	if err != nil {
		t.Fatal(err)
	}
	var mean, sumSq float64
	for _, v := range seq {
		if v < -1 || v >= 1 {
			t.Fatalf("抖动超出 [-1,1) 支撑域: %v", v)
		}
		mean += v
		sumSq += v * v
	}
	mean /= float64(len(seq))
	rms := math.Sqrt(sumSq / float64(len(seq)))
	// 样本均值标准误约 1/sqrt(6N) ≈ 1.6e-3，取 4 倍标准误留统计余量
	if math.Abs(mean) > 4.0/math.Sqrt(6*float64(len(seq))) {
		t.Fatalf("TPDF 均值偏离零: %v", mean)
	}
	want := 1 / math.Sqrt(6)
	if math.Abs(rms-want) > 2e-3 {
		t.Fatalf("TPDF RMS %.6f 偏离理论值 %.6f", rms, want)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
