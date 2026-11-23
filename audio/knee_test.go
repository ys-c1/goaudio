package audio

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{ThresholdDB: -24, Ratio: 4, KneeDB: 8, Duration: 10 * time.Millisecond}
}

// 门限为零、比率为负、时长为零三类输入必须报错，并指出字段。
func TestConfigErrorsNameFields(t *testing.T) {
	cases := []struct {
		name  string
		cfg   Config
		field string
		want  error
	}{
		{"门限为零", Config{ThresholdDB: 0, Ratio: 4, Duration: time.Millisecond}, "threshold", ErrThreshold},
		{"门限为正", Config{ThresholdDB: 3, Ratio: 4, Duration: time.Millisecond}, "threshold", ErrThreshold},
		{"比率为负", Config{ThresholdDB: -24, Ratio: -2, Duration: time.Millisecond}, "ratio", ErrRatio},
		{"时长为零", Config{ThresholdDB: -24, Ratio: 4, Duration: 0}, "duration", ErrDuration},
	}
	c, err := NewClip(8000, 800)
	if err != nil {
		t.Fatal(err)
	}
	if err := Sine(c, 440, 8000); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Process(c, tc.cfg)
			assertFieldError(t, err, tc.field, tc.want)

			if err := tc.cfg.Validate(); err == nil {
				t.Fatal("Validate 应报错")
			} else {
				assertFieldError(t, err, tc.field, tc.want)
			}

			_, err = MeasureLevelPairs([]float64{-20}, tc.cfg, 8000, 0.1)
			assertFieldError(t, err, tc.field, tc.want)
		})
	}
}

func assertFieldError(t *testing.T, err error, field string, want error) {
	t.Helper()
	if err == nil {
		t.Fatal("期望报错，实际为 nil")
	}
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("错误类型应为 *ConfigError，实际 %T: %v", err, err)
	}
	if ce.Field != field {
		t.Fatalf("错误应指出字段 %q，实际 %q", field, ce.Field)
	}
	if !errors.Is(err, want) {
		t.Fatalf("错误应包裹 %v，实际 %v", want, err)
	}
	if !strings.Contains(err.Error(), field) {
		t.Fatalf("错误信息应包含字段名 %q: %v", field, err)
	}
}

// 原有硬拐点接口保持不变。
func TestHardKneeUnchanged(t *testing.T) {
	cases := []struct {
		in, want float64
	}{
		{-40, 0},
		{-24, 0},
		{-6, -13.5}, // (1/4-1)*(-6+24)
		{0, -18},
	}
	for _, tc := range cases {
		if got := HardKneeGainDB(tc.in, -24, 4); math.Abs(got-tc.want) > 1e-12 {
			t.Fatalf("硬拐点在 %.1f dB 处为 %.12f，期望 %.12f", tc.in, got, tc.want)
		}
	}
	if got := SoftKneeGainDB(-6, -24, 4, 0); got != HardKneeGainDB(-6, -24, 4) {
		t.Fatalf("宽度为零应退回硬拐点，得到 %g", got)
	}
}

// 软拐点在两侧边界处取值与一阶导数都连续（C1）。
func TestSoftKneeC1Continuity(t *testing.T) {
	const T, R, W = -24.0, 4.0, 8.0
	lo, hi := T-W/2, T+W/2

	y := func(x float64) float64 { return SoftKneeOutputDB(x, T, R, W) }
	const h = 1e-6
	numeric := func(x float64) float64 { return (y(x+h) - y(x-h)) / (2 * h) }

	for _, b := range []float64{lo, hi} {
		left := (y(b) - y(b-h)) / h
		right := (y(b+h) - y(b)) / h
		if math.Abs(left-right) > 1e-4 {
			t.Fatalf("拐点 x=%.1f 处斜率跳变：左 %.9f 右 %.9f", b, left, right)
		}
		if d := math.Abs(numeric(b) - SoftKneeSlope(b, T, R, W)); d > 1e-5 {
			t.Fatalf("拐点 x=%.1f 处解析斜率 %.9f 与数值斜率 %.9f 不符",
				b, SoftKneeSlope(b, T, R, W), numeric(b))
		}
	}
	if got, want := SoftKneeSlope(lo, T, R, W), 1.0; math.Abs(got-want) > 1e-12 {
		t.Fatalf("拐点下边界斜率 %.12f，期望 %.12f", got, want)
	}
	if got, want := SoftKneeSlope(hi, T, R, W), 1/R; math.Abs(got-want) > 1e-12 {
		t.Fatalf("拐点上边界斜率 %.12f，期望 %.12f", got, want)
	}
	// 门限中心处曲率应与解析式一致。
	const eps = 1e-5
	kNum := (SoftKneeSlope(T+eps, T, R, W) - SoftKneeSlope(T-eps, T, R, W)) / (2 * eps)
	if d := math.Abs(kNum - SoftKneeCurvature(R, W)); d > 1e-6 {
		t.Fatalf("拐点曲率数值 %.9f 与解析 %.9f 不符", kNum, SoftKneeCurvature(R, W))
	}
}

// 拐点两侧斜率分别核验，任一侧越界都要报错。
func TestSlopeLimitsVerifiedPerSide(t *testing.T) {
	fit := KneeFit{LeftSlope: 1.000, RightSlope: 0.250}

	if err := fit.VerifySlopes(SlopeLimits{Min: 0.2, Max: 1.05}); err != nil {
		t.Fatalf("两侧都在区间内，不应报错: %v", err)
	}

	err := fit.VerifySlopes(SlopeLimits{Min: 0.2, Max: 0.9})
	if !errors.Is(err, ErrSlope) || !strings.Contains(err.Error(), "左侧") {
		t.Fatalf("应仅左侧越界，实际: %v", err)
	}

	err = fit.VerifySlopes(SlopeLimits{Min: 0.3, Max: 1.05})
	if !errors.Is(err, ErrSlope) || !strings.Contains(err.Error(), "右侧") {
		t.Fatalf("应仅右侧越界，实际: %v", err)
	}

	if err := fit.VerifySlopes(SlopeLimits{Min: 0.9, Max: 0.2}); !errors.Is(err, ErrSlope) {
		t.Fatalf("区间颠倒应报错，实际: %v", err)
	}
}

// 由实测对应反解拐点，并核验位置、曲率、两侧斜率。
func TestKneeFitFromMeasurements(t *testing.T) {
	cfg := testConfig()
	levels := makeLevels(-54, -6, 2)
	pairs, err := MeasureLevelPairs(levels, cfg, 48000, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	fit, err := FitSoftKnee(pairs)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("反解拐点 T=%.4f dBFS, W=%.4f dB, κ=%.5f /dB, 左斜率=%.4f, 右斜率=%.4f, RMSE=%.5f dB",
		fit.PositionDB, fit.KneeDB, fit.Curvature, fit.LeftSlope, fit.RightSlope, fit.RMSE)

	if math.Abs(fit.PositionDB-cfg.ThresholdDB) > 0.05 {
		t.Fatalf("拐点位置 %.4f 偏离门限 %.1f 超过 0.05 dB", fit.PositionDB, cfg.ThresholdDB)
	}
	if math.Abs(fit.KneeDB-cfg.KneeDB) > 0.2 {
		t.Fatalf("拐点宽度 %.4f 偏离设定 %.1f 超过 0.2 dB", fit.KneeDB, cfg.KneeDB)
	}
	wantK := SoftKneeCurvature(cfg.Ratio, cfg.KneeDB)
	if math.Abs(fit.Curvature-wantK) > 0.005*math.Abs(wantK) {
		t.Fatalf("曲率 %.6f 偏离解析 %.6f", fit.Curvature, wantK)
	}
	if fit.RMSE > 0.01 {
		t.Fatalf("拟合 RMSE %.5f dB 过大", fit.RMSE)
	}
	if err := fit.VerifySlopes(SlopeLimits{Min: 0.2, Max: 1.05}); err != nil {
		t.Fatalf("两侧斜率应落在 [0.2, 1.05]: %v", err)
	}
	if math.Abs(fit.LeftSlope-1) > 0.01 {
		t.Fatalf("左侧斜率 %.4f 应约为 1", fit.LeftSlope)
	}
	if math.Abs(fit.RightSlope-1/fit.Ratio) > 0.01 {
		t.Fatalf("右侧斜率 %.4f 应约为 1/R=%.4f（反解比）", fit.RightSlope, 1/fit.Ratio)
	}
	// 压缩比也由数据独立解出（未作为已知量传入）。
	if math.Abs(fit.Ratio-cfg.Ratio) > 0.05*cfg.Ratio {
		t.Fatalf("反解压缩比 %.4f 偏离设定 %.1f", fit.Ratio, cfg.Ratio)
	}
}

// 固定压缩比的入口应与联合反解给出同样的拐点。
func TestKneeFitWithKnownRatio(t *testing.T) {
	cfg := testConfig()
	pairs, err := MeasureLevelPairs(makeLevels(-54, -6, 2), cfg, 48000, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	free, err := FitSoftKnee(pairs)
	if err != nil {
		t.Fatal(err)
	}
	fixed, err := FitSoftKneeRatio(pairs, cfg.Ratio)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(free.PositionDB-fixed.PositionDB) > 0.02 || math.Abs(free.KneeDB-fixed.KneeDB) > 0.05 {
		t.Fatalf("两种入口结果不一致：free %+v fixed %+v", free, fixed)
	}
}

// 数据不足时必须明确报错，而不是返回一个任意拐点。
func TestFitRejectsInsufficientData(t *testing.T) {
	if _, err := FitSoftKnee(nil); err == nil {
		t.Fatal("空数据应报错")
	}
	if _, err := FitSoftKnee([]LevelPair{{-30, -30}, {-25, -25}}); err == nil {
		t.Fatal("过少数据应报错")
	}
	// 全程无压缩：不能反解出有意义的拐点。
	flat := []LevelPair{{-40, -40}, {-30, -30}, {-20, -20}, {-10, -10}}
	if _, err := FitSoftKnee(flat); !errors.Is(err, ErrIdentify) {
		t.Fatalf("无压缩数据应报 ErrIdentify，实际 %v", err)
	}
	// 只有拐点区内数据，W 与 R 不可分离。
	kneeOnly := []LevelPair{{-26, -26.19}, {-24, -24.75}, {-22, -23.69}}
	if _, err := FitSoftKnee(kneeOnly); !errors.Is(err, ErrSlopeData) {
		t.Fatalf("只有拐点区数据应报 ErrSlopeData，实际 %v", err)
	}
}

// 同一素材两次运行，增益曲线必须逐位相同；反解结果也必须可重复。
func TestDeterministicGainCurve(t *testing.T) {
	cfg := testConfig()
	c := crescendoClip(t, 48000, 1.0, -48, -6)

	first, err := Process(c, cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Process(c, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.GainDB) != len(second.GainDB) {
		t.Fatalf("两次增益曲线长度不同 %d %d", len(first.GainDB), len(second.GainDB))
	}
	for i := range first.GainDB {
		if first.GainDB[i] != second.GainDB[i] {
			t.Fatalf("第 %d 帧增益不同：%v vs %v", i, first.GainDB[i], second.GainDB[i])
		}
		if first.Frames[i] != second.Frames[i] {
			t.Fatalf("第 %d 帧样本不同：%d vs %d", i, first.Frames[i], second.Frames[i])
		}
	}

	pairs, err := MeasureLevelPairs(makeLevels(-36, -6, 2), cfg, 48000, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	f1, err := FitSoftKnee(pairs)
	if err != nil {
		t.Fatal(err)
	}
	f2, err := FitSoftKnee(pairs)
	if err != nil {
		t.Fatal(err)
	}
	if f1 != f2 {
		t.Fatalf("两次反解结果不同：%+v vs %+v", f1, f2)
	}
}

// 由目标压缩量反解拐点位置，实测值与反解值之差必须不超过上界。
func TestInverseReductionAccuracy(t *testing.T) {
	cfg := testConfig()
	pairs, err := MeasureLevelPairs(makeLevels(-54, -6, 0.5), cfg, 48000, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	targets := []float64{0.25, 0.5, 1, 2, 3, 4.5, 6, 9, 12}
	worst := 0.0
	for _, g := range targets {
		solved, err := KneeInputForReduction(g, cfg.ThresholdDB, cfg.Ratio, cfg.KneeDB)
		if err != nil {
			t.Fatalf("目标 %.2f dB 反解失败: %v", g, err)
		}
		measured, err := MeasuredKneeInput(pairs, g)
		if err != nil {
			t.Fatalf("目标 %.2f dB 实测插值失败: %v", g, err)
		}
		d := math.Abs(measured - solved)
		if d > worst {
			worst = d
		}
		if d > KneeInverseErrorBoundDB {
			t.Fatalf("目标 %.2f dB：实测 %.4f 与反解 %.4f 相差 %.4f，超过上界 %.4f",
				g, measured, solved, d, KneeInverseErrorBoundDB)
		}
		// 反解点代回曲线应真的达到目标压缩量。
		if got := -SoftKneeGainDB(solved, cfg.ThresholdDB, cfg.Ratio, cfg.KneeDB); math.Abs(got-g) > 1e-6 {
			t.Fatalf("反解点 x=%.4f 的压缩量 %.9f 不等于目标 %.9f", solved, got, g)
		}
	}
	t.Logf("反解与实测最大差值 %.5f dB（上界 %.3f dB）", worst, KneeInverseErrorBoundDB)
}

// 轻、响、脉冲、渐强、静音五类素材。
const reportRate = 48000

func TestUseCases(t *testing.T) {
	cfg := testConfig()
	t.Run("轻", func(t *testing.T) {
		c := steadySine(t, reportRate, 1.0, -30)
		res, err := Process(c, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if max := maxGain(res.GainDB); max > 1e-12 {
			t.Fatalf("轻素材不应压缩，最大增益 %.6f dB", max)
		}
		if got := tailRMSFromFrames(res.Frames); math.Abs(got+30) > 0.2 {
			t.Fatalf("轻素材输出 %.2f dBFS 应约为 -30", got)
		}
	})

	t.Run("响", func(t *testing.T) {
		c := steadySine(t, reportRate, 1.0, -6)
		res, err := Process(c, cfg)
		if err != nil {
			t.Fatal(err)
		}
		g := tailGainDB(res)
		want := SoftKneeGainDB(-6, cfg.ThresholdDB, cfg.Ratio, cfg.KneeDB)
		if math.Abs(g-want) > 0.2 {
			t.Fatalf("响素材稳态增益 %.3f dB，期望 %.3f", g, want)
		}
		out := tailRMSFromFrames(res.Frames)
		if wantOut := cfg.ThresholdDB + (-6-cfg.ThresholdDB)/cfg.Ratio; math.Abs(out-wantOut) > 0.3 {
			t.Fatalf("响素材输出 %.2f dBFS，期望约 %.2f", out, wantOut)
		}
		if in := tailRMSFromFrames(c.Frames); out >= in {
			t.Fatalf("响素材未被压低：输入 %.2f 输出 %.2f", in, out)
		}
	})

	t.Run("脉冲", func(t *testing.T) {
		c := impulseClip(t, reportRate, 1.0, 100*reportRate/1000) // 100 ms 满幅脉冲
		res, err := Process(c, cfg)
		if err != nil {
			t.Fatal(err)
		}
		// 检测窗为因果窗，起音前段尚未建立增益；取脉冲后段的峰值核验。
		start := len(c.Frames) / 2
		from := start + len(res.Frames)/20
		settled := &Clip{Rate: reportRate, Frames: res.Frames[from:]}
		inPeak := Peak(&Clip{Rate: reportRate, Frames: c.Frames[start : from+len(settled.Frames)]})
		outPeak := Peak(settled)
		// 检测量为 RMS，满幅方波状正弦的 RMS 约 -1.5 dBFS，
		// 期望增益由该实测电平决定，而不是峰值电平。
		detected := rmsDB(c.Frames[start:from])
		want := SoftKneeGainDB(detected, cfg.ThresholdDB, cfg.Ratio, cfg.KneeDB)
		if db := 20 * math.Log10(float64(outPeak)/float64(inPeak)); math.Abs(db) < 10 {
			t.Fatalf("脉冲稳定段峰值只压低 %.2f dB：输入 %d 输出 %d", -db, inPeak, outPeak)
		} else if math.Abs(db-want) > 0.5 {
			t.Fatalf("脉冲稳定段增益 %.2f dB，期望 %.2f（检测电平 %.2f dBFS）", db, want, detected)
		}
		// 脉冲结束后应恢复到零增益。
		if g := res.GainDB[len(res.GainDB)-1]; g != 0 {
			t.Fatalf("脉冲结束后增益应回到 0，实际 %.6f", g)
		}
	})

	t.Run("渐强", func(t *testing.T) {
		c := crescendoClip(t, reportRate, 2.0, -48, -6)
		res, err := Process(c, cfg)
		if err != nil {
			t.Fatal(err)
		}
		// 增益应随输入变响单调下降（压缩量单调上升）。
		for i := 1; i < len(res.GainDB); i++ {
			if res.GainDB[i] > res.GainDB[i-1]+1e-3 {
				t.Fatalf("第 %d 帧增益回升：%.6f -> %.6f", i, res.GainDB[i-1], res.GainDB[i])
			}
		}
		if first, last := res.GainDB[0], res.GainDB[len(res.GainDB)-1]; last >= first-6 {
			t.Fatalf("渐强末端增益 %.2f dB 应明显低于起始 %.2f dB", last, first)
		}
	})

	t.Run("静音", func(t *testing.T) {
		c, _ := NewClip(reportRate, reportRate/2)
		res, err := Process(c, cfg)
		if err != nil {
			t.Fatal(err)
		}
		for i, g := range res.GainDB {
			if g != 0 || math.IsNaN(g) {
				t.Fatalf("静音第 %d 帧增益应为 0，实际 %v", i, g)
			}
			if res.Frames[i] != 0 {
				t.Fatalf("静音第 %d 帧输出应为 0，实际 %d", i, res.Frames[i])
			}
		}
	})
}

func steadySine(t *testing.T, rate int, seconds, levelDB float64) *Clip {
	t.Helper()
	n := int(math.Round(float64(rate) * seconds))
	c, err := NewClip(rate, n)
	if err != nil {
		t.Fatal(err)
	}
	if err := Sine(c, probeFreq(rate), sinePeakForRMSLevel(levelDB)); err != nil {
		t.Fatal(err)
	}
	return c
}

func crescendoClip(t *testing.T, rate int, seconds, fromDB, toDB float64) *Clip {
	t.Helper()
	n := int(math.Round(float64(rate) * seconds))
	c, err := NewClip(rate, n)
	if err != nil {
		t.Fatal(err)
	}
	for i := range c.Frames {
		frac := float64(i) / float64(n-1)
		level := fromDB + (toDB-fromDB)*frac
		amp := float64(sinePeakForRMSLevel(level))
		tSec := float64(i) / float64(rate)
		c.Frames[i] = int16(amp * math.Sin(2*math.Pi*probeFreq(rate)*tSec))
	}
	return c
}

func impulseClip(t *testing.T, rate int, seconds float64, burst int) *Clip {
	t.Helper()
	n := int(math.Round(float64(rate) * seconds))
	c, err := NewClip(rate, n)
	if err != nil {
		t.Fatal(err)
	}
	start := n / 2
	for i := 0; i < burst && start+i < n; i++ {
		c.Frames[start+i] = int16(32767 * math.Sin(2*math.Pi*probeFreq(rate)*float64(i)/float64(rate)))
	}
	return c
}

// probeFreq 取与检测窗整数个周期的频率，避免 RMS 纹波。
func probeFreq(rate int) float64 {
	w := levelWindowFrames(rate, 10*time.Millisecond)
	cycles := math.Round(440 * float64(w) / float64(rate))
	if cycles < 1 {
		cycles = 1
	}
	return cycles * float64(rate) / float64(w)
}

func makeLevels(from, to, step float64) []float64 {
	var out []float64
	for v := from; v <= to+1e-9; v += step {
		out = append(out, math.Round(v*1000)/1000)
	}
	return out
}

func maxGain(g []float64) float64 {
	m := math.Inf(-1)
	for _, v := range g {
		if v > m {
			m = v
		}
	}
	return m
}

func tailGainDB(res *Result) float64 {
	n := len(res.GainDB)
	w := 480
	if w > n {
		w = n
	}
	var s float64
	for _, v := range res.GainDB[n-w:] {
		s += v
	}
	return s / float64(w)
}

func tailRMSFromFrames(frames []int16) float64 {
	w := 480
	if w > len(frames) {
		w = len(frames)
	}
	return rmsDB(frames[len(frames)-w:])
}

// TestKneeReport 打印拐点与压缩量对照表，供收尾报告引用。
func TestKneeReport(t *testing.T) {
	cfg := testConfig()
	pairs, err := MeasureLevelPairs(makeLevels(-54, -6, 2), cfg, reportRate, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	fit, err := FitSoftKnee(pairs)
	if err != nil {
		t.Fatal(err)
	}

	t.Log("== 两档输入的压缩量 ==")
	t.Log("| 档位 | 输入 x (dBFS) | 输出 y (dBFS) | 压缩量 (dB) |")
	t.Log("|---|---:|---:|---:|")
	for _, tc := range []struct {
		name  string
		level float64
	}{{"轻档 -30 dBFS", -30}, {"响档 -6 dBFS", -6}} {
		c := steadySine(t, reportRate, 1.0, tc.level)
		res, err := Process(c, cfg)
		if err != nil {
			t.Fatal(err)
		}
		in := tailRMSFromFrames(c.Frames)
		out := tailRMSFromFrames(res.Frames)
		t.Logf("| %s | %.3f | %.3f | %.3f |", tc.name, in, out, in-out)
	}

	t.Log("== 目标压缩量反解拐点位置 ==")
	t.Logf("上界 %.3f dB", KneeInverseErrorBoundDB)
	t.Log("| 目标压缩量 (dB) | 反解位置 x* (dBFS) | 实测位置 x (dBFS) | 差 (dB) |")
	t.Log("|---:|---:|---:|---:|")
	fine, err := MeasureLevelPairs(makeLevels(-54, -6, 0.5), cfg, reportRate, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range []float64{0.5, 1, 2, 3, 6, 9, 12} {
		solved, err := KneeInputForReduction(g, cfg.ThresholdDB, cfg.Ratio, cfg.KneeDB)
		if err != nil {
			t.Fatal(err)
		}
		measured, err := MeasuredKneeInput(fine, g)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("| %.2f | %.3f | %.3f | %.4f |", g, solved, measured, math.Abs(measured-solved))
	}

	t.Log("== 数值表（x 输入, y 输出）与拐点坐标 ==")
	t.Log("| x (dBFS) | y (dBFS) | 增益 (dB) | 压缩量 (dB) |")
	t.Log("|---:|---:|---:|---:|")
	lo, hi := cfg.ThresholdDB-cfg.KneeDB/2, cfg.ThresholdDB+cfg.KneeDB/2
	for _, p := range pairs {
		mark := ""
		if math.Abs(p.InputDB-lo) < 1e-6 || math.Abs(p.InputDB-cfg.ThresholdDB) < 1e-6 || math.Abs(p.InputDB-hi) < 1e-6 {
			mark = " ◄"
		}
		if math.Abs(p.InputDB-lo) < 0.6 || math.Abs(p.InputDB-cfg.ThresholdDB) < 0.6 || math.Abs(p.InputDB-hi) < 0.6 || p.InputDB <= -42 || p.InputDB >= -10 {
			t.Logf("| %.3f | %.3f | %.3f | %.3f |%s", p.InputDB, p.OutputDB, p.OutputDB-p.InputDB, p.InputDB-p.OutputDB, mark)
		}
	}
	t.Logf("拐点坐标：下边界 (%.2f, %.2f)，中心 (%.2f, %.2f)，上边界 (%.2f, %.2f)；反解 T=%.3f dBFS，曲率 %.4f /dB，左斜率 %.3f，右斜率 %.3f",
		lo, SoftKneeOutputDB(lo, cfg.ThresholdDB, cfg.Ratio, cfg.KneeDB),
		cfg.ThresholdDB, SoftKneeOutputDB(cfg.ThresholdDB, cfg.ThresholdDB, cfg.Ratio, cfg.KneeDB),
		hi, SoftKneeOutputDB(hi, cfg.ThresholdDB, cfg.Ratio, cfg.KneeDB),
		fit.PositionDB, fit.Curvature, fit.LeftSlope, fit.RightSlope)
}
