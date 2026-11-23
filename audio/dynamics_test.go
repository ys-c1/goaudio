package audio

import (
	"errors"
	"path/filepath"
	"testing"
)

// 一阶时间常数必须从实测阶跃响应拟合，且高 R²、接近标称时间常数。
func TestMeasureTimeConstantsFit(t *testing.T) {
	l, err := NewGainLimiter(1000, 50, 200)
	if err != nil {
		t.Fatal(err)
	}
	tc, err := l.MeasureTimeConstants(48000, 4096, 4000)
	if err != nil {
		t.Fatal(err)
	}
	if tc.AttackR2 < 0.999 || tc.ReleaseR2 < 0.999 {
		t.Fatalf("拟合优度过低 R²=%f/%f", tc.AttackR2, tc.ReleaseR2)
	}
	if !near(tc.AttackTauSamples, 50, 1.0) || !near(tc.ReleaseTauSamples, 200, 2.0) {
		t.Fatalf("拟合时间常数偏离标称值: %+v", tc)
	}
	if tc.AttackTauSeconds <= 0 || tc.ReleaseTauSeconds <= 0 {
		t.Fatal("秒级时间常数应为正")
	}
}

// 退化曲线（系数为 1，一帧到位、无指数段）必须报拟合退化，而不是返回经验值。
func TestFitDegenerate(t *testing.T) {
	l, err := NewGainLimiter(1000, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.MeasureTimeConstants(48000, 64, 4000); !errors.Is(err, ErrFitDegenerate) {
		t.Fatalf("退化一阶响应应报 ErrFitDegenerate，得到 %v", err)
	}
}

func near(a, b, tol float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= tol
}

// 五类素材各一段：反解结果必须同时满足过冲与纹波两条独立约束。
func TestSolveOnFiveMaterials(t *testing.T) {
	const rate, frames, T = 48000, 24000, 10000
	const overBudget, rippleBudget = 800.0, 1500.0
	kinds := []MaterialKind{
		MaterialDrum, MaterialVocal, MaterialStrings, MaterialSilence, MaterialTiny,
	}
	durations := map[MaterialKind]int{
		MaterialDrum: frames, MaterialVocal: frames, MaterialStrings: frames,
		MaterialSilence: frames, MaterialTiny: 8,
	}
	registry := NewRegistry()
	for _, kind := range kinds {
		dur := durations[kind]
		clip, err := MakeMaterial(kind, rate, dur, T)
		if err != nil {
			t.Fatalf("%s 素材生成失败: %v", kind, err)
		}
		levels := AbsLevels(clip)
		rep, err := SolveAdaptiveTimes(rate, dur, T, levels, overBudget, rippleBudget)
		if err != nil {
			t.Fatalf("%s 反解失败: %v", kind, err)
		}
		registry.Register(kind, rep)

		// 时间常数必须是实测拟合出来的正数。
		if rep.Tau.AttackTauSamples <= 0 || rep.Tau.ReleaseTauSamples <= 0 {
			t.Fatalf("%s 时间常数非正: %+v", kind, rep.Tau)
		}
		if rep.Tau.AttackR2 < 0.99 || rep.Tau.ReleaseR2 < 0.99 {
			t.Fatalf("%s 拟合优度过低: %+v", kind, rep.Tau)
		}

		if rep.Limited {
			// 起音过冲、释放纹波两条分开验，任一超标都要点名失败。
			mo, err := VerifyAttackOvershoot(T, rep.AttackSamples, rep.ReleaseSamples, levels, overBudget)
			if err != nil {
				t.Fatalf("%s 起音过冲验证失败 实测=%v: %v", kind, mo, err)
			}
			mr, err := VerifyReleaseRipple(T, rep.AttackSamples, rep.ReleaseSamples, levels, rippleBudget)
			if err != nil {
				t.Fatalf("%s 释放纹波验证失败 实测=%v: %v", kind, mr, err)
			}
		}
	}
	if len(registry.Entries) != 5 {
		t.Fatalf("登记表应有 5 条，实际 %d", len(registry.Entries))
	}

	// 收尾登记表 JSON 往返。
	p := filepath.Join(t.TempDir(), "table.json")
	if err := registry.WriteJSON(p); err != nil {
		t.Fatal(err)
	}
	back, err := LoadRegistry(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Entries) != 5 || back.Entries[0].Material != string(MaterialDrum) {
		t.Fatalf("登记表往返内容异常: %+v", back.Entries)
	}
}

// 反解的核心保证：解出的起音 N 是“仍满足过冲预算”的最大样本数，
// 即 N 达标而 N+1 必超标（最优边界），释放同理。这与素材快慢无关、
// 由实测整段信号直接判定，替代对具体素材排序的主观假设。
func TestSolvedTimesAreMaximal(t *testing.T) {
	const rate, frames, T = 48000, 24000, 10000
	const overBudget, rippleBudget = 800.0, 1500.0
	for _, kind := range []MaterialKind{MaterialDrum, MaterialVocal, MaterialStrings} {
		clip, err := MakeMaterial(kind, rate, frames, T)
		if err != nil {
			t.Fatal(err)
		}
		levels := AbsLevels(clip)
		rep, err := SolveAdaptiveTimes(rate, frames, T, levels, overBudget, rippleBudget)
		if err != nil {
			t.Fatalf("%s 反解失败: %v", kind, err)
		}
		// 起音：解出的 N 达标；再慢一帧就应突破过冲预算。
		if _, err := VerifyAttackOvershoot(T, rep.AttackSamples, rep.ReleaseSamples, levels, overBudget); err != nil {
			t.Fatalf("%s 解出的起音 N=%d 本应达标: %v", kind, rep.AttackSamples, err)
		}
		if rep.AttackSamples < frames {
			if _, err := VerifyAttackOvershoot(T, rep.AttackSamples+1, rep.ReleaseSamples, levels, overBudget); !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("%s 起音 N=%d 不是最大可行值，N+1 仍达标", kind, rep.AttackSamples)
			}
		}
		// 释放：解出的 N 达标；再慢一帧就应突破纹波预算。
		if _, err := VerifyReleaseRipple(T, rep.AttackSamples, rep.ReleaseSamples, levels, rippleBudget); err != nil {
			t.Fatalf("%s 解出的释放 N=%d 本应达标: %v", kind, rep.ReleaseSamples, err)
		}
		if rep.ReleaseSamples < frames {
			if _, err := VerifyReleaseRipple(T, rep.AttackSamples, rep.ReleaseSamples+1, levels, rippleBudget); !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("%s 释放 N=%d 不是最大可行值，N+1 仍达标", kind, rep.ReleaseSamples)
			}
		}
	}
}

// 反解必须随素材变化（不再是固定起停）：三类越阈素材的起音或释放时间至少有差异。
func TestAdaptiveTimesDifferAcrossMaterials(t *testing.T) {
	const rate, frames, T = 48000, 24000, 10000
	seen := map[[2]int]bool{}
	for _, kind := range []MaterialKind{MaterialDrum, MaterialVocal, MaterialStrings} {
		clip, _ := MakeMaterial(kind, rate, frames, T)
		rep, err := SolveAdaptiveTimes(rate, frames, T, AbsLevels(clip), 800, 1500)
		if err != nil {
			t.Fatal(err)
		}
		seen[[2]int{rep.AttackSamples, rep.ReleaseSamples}] = true
	}
	if len(seen) < 2 {
		t.Fatalf("起停时间应随素材自适应，实际全部相同: %v", seen)
	}
}

// 三种零值入参必须拒绝，且错误要能点名到具体字段。
func TestZeroFieldsRejected(t *testing.T) {
	levels := []float64{1, 2, 30000, 30000, 2, 1}
	cases := []struct {
		name string
		call func() error
		want error
	}{
		{"采样率为零", func() error {
			_, e := SolveAdaptiveTimes(0, 6, 10000, levels, 800, 1500)
			return e
		}, ErrZeroSampleRate},
		{"时长为零", func() error {
			_, e := SolveAdaptiveTimes(48000, 0, 10000, levels, 800, 1500)
			return e
		}, ErrZeroDuration},
		{"过冲为零", func() error {
			_, e := SolveAdaptiveTimes(48000, 6, 10000, levels, 0, 1500)
			return e
		}, ErrZeroOvershoot},
		{"纹波为零", func() error {
			_, e := SolveAdaptiveTimes(48000, 6, 10000, levels, 800, 0)
			return e
		}, ErrZeroRipple},
	}
	for _, tc := range cases {
		err := tc.call()
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: 期望 %v，得到 %v", tc.name, tc.want, err)
		}
	}

	// 验证接口本身也要拒绝零过冲/零纹波并点名字段。
	if _, err := VerifyAttackOvershoot(10000, 4, 4, levels, 0); !errors.Is(err, ErrZeroOvershoot) {
		t.Fatalf("零过冲验证应报 ErrZeroOvershoot，得到 %v", err)
	}
	if _, err := VerifyReleaseRipple(10000, 4, 4, levels, 0); !errors.Is(err, ErrZeroRipple) {
		t.Fatalf("零纹波验证应报 ErrZeroRipple，得到 %v", err)
	}
}

// 超过约束的起/停时间应被独立验证判为失败。
func TestVerifyDetectsViolation(t *testing.T) {
	levels := []float64{1, 1, 30000, 30000, 1, 1}
	// 很慢的起音（256 帧）必然产生大过冲。
	if _, err := VerifyAttackOvershoot(10000, 256, 256, levels, 10); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("过冲超标应报 ErrLimitExceeded，得到 %v", err)
	}
	// 极慢释放 + 紧纹波预算必然超标。低电平取接近阈值的 9000，
	// 使释放恢复期（增益仍约 0.33）的纹波足够大、能突破预算。
	if _, err := VerifyReleaseRipple(10000, 1, 256, []float64{9000, 9000, 30000, 30000, 9000, 9000}, 100); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("纹波超标应报 ErrLimitExceeded，得到 %v", err)
	}
}

// 原有固定起停接口保持不变：签名与行为都不动。
func TestFixedInterfacePreserved(t *testing.T) {
	c, _ := NewClip(8000, 100)
	for i := range c.Frames {
		c.Frames[i] = 1000
	}
	if err := ApplyEnvelope(c, 10, 10); err != nil {
		t.Fatal(err)
	}
	if c.Frames[0] != 0 || c.Frames[99] != 0 {
		t.Fatalf("固定淡入淡出端点行为被破坏 %d %d", c.Frames[0], c.Frames[99])
	}
	// 新增的固定参数压限接口同样以样本数给参，不改变既有语义。
	l, err := NewGainLimiter(10000, 4, 16)
	if err != nil {
		t.Fatal(err)
	}
	out := l.Limit([]float64{100, 30000, 30000, 100, 100})
	if len(out) != 5 {
		t.Fatalf("压限输出长度错误 %d", len(out))
	}
}

// 静音素材不越阈：报告标记无需压限，起停为零，但仍给出实测时间常数。
func TestSilenceNotLimited(t *testing.T) {
	clip, err := MakeMaterial(MaterialSilence, 48000, 1024, 10000)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := SolveAdaptiveTimes(48000, 1024, 10000, AbsLevels(clip), 800, 1500)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Limited {
		t.Fatal("静音不应触发压限")
	}
	if rep.AttackSamples != 0 || rep.ReleaseSamples != 0 {
		t.Fatalf("静音起停应为零，得到 %d/%d", rep.AttackSamples, rep.ReleaseSamples)
	}
	if rep.Tau.AttackTauSamples <= 0 {
		t.Fatal("静音也应给出实测时间常数")
	}
}
