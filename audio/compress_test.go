package audio

import (
	"errors"
	"math"
	"testing"
)

// ---- 独立参考实现：与 compress.go 采用不同的代码组织与取整路径，
// 用于逐样本对照并给出峰值误差上界。 ----
func refCompress(dst, src *Clip, cfg CompressorConfig) {
	fs := float64(src.Rate)
	tauA := cfg.AttackMs / 1000 * fs
	tauR := cfg.ReleaseMs / 1000 * fs
	ca := 1.0
	if tauA > 0 {
		ca = 1 - math.Exp(-1/tauA)
	}
	cr := 1.0
	if tauR > 0 {
		cr = 1 - math.Exp(-1/tauR)
	}
	tDB := 20 * math.Log10(float64(cfg.Threshold)/32768)
	wh := cfg.KneeDB / 2
	invR := 0.0
	if cfg.Ratio > 0 {
		invR = 1 / cfg.Ratio
	}
	env := 1.0 / 32768.0
	n := len(src.Frames)
	lookahead := int(math.Round(cfg.AttackMs / 1000 * float64(src.Rate)))
	// 第一遍：独立路径求每帧线性增益。
	gains := make([]float64, n)
	for i := 0; i < n; i++ {
		d := math.Abs(float64(src.Frames[i]))
		if d < 1.0/32768.0 {
			d = 1.0 / 32768.0
		}
		c := ca
		if d < env {
			c = cr
		}
		env += c * (d - env)
		x := 20 * math.Log10(env/32768)
		var gDB float64
		delta := x - tDB
		if wh <= 0 {
			if delta > 0 {
				gDB = -(1 - invR) * delta
			}
		} else if delta <= -wh {
			gDB = 0
		} else if delta >= wh {
			gDB = -(1 - invR) * delta
		} else {
			u := delta + wh
			gDB = -(1 - invR) * u * u / (4 * wh)
		}
		gains[i] = math.Pow(10, gDB/20)
	}
	// 第二遍：前瞻施加，末尾冻结。
	for i, s := range src.Frames {
		j := i + lookahead
		if j >= n {
			j = n - 1
		}
		y := float64(s) * gains[j]
		// 参考侧用 Floor(x+0.5) 而非 math.Round，构成独立取整路径。
		v := int(math.Floor(y + 0.5))
		if v > 32767 {
			v = 32767
		}
		if v < -32768 {
			v = -32768
		}
		dst.Frames[i] = int16(v)
	}
}

// 手算关键点：门限 16384（=-6.0206 dBFS），膝宽 6dB，比率 4:1。
func TestSoftKneeHandCalc(t *testing.T) {
	tDB := 20 * math.Log10(0.5) // -6.0205999...
	cases := []struct {
		inDB, want float64
	}{
		{-20, 0},                      // 膝下：不压缩
		{tDB - 3, 0},                  // 膝下界点：增益 0
		{tDB, -0.5625},                // 门限处：-(3/4)*3²/12
		{tDB + 3, -2.25},              // 膝上界点：-(3/4)*3
		{0, -(1 - 1.0/4) * (0 - tDB)}, // 满幅：全比率段
	}
	const eps = 1e-9
	for _, tc := range cases {
		got := softKneeGainDB(tc.inDB, tDB, 3, 4)
		if math.Abs(got-tc.want) > eps {
			t.Fatalf("输入 %.4f dB：手算 %.6f dB，实得 %.6f dB", tc.inDB, tc.want, got)
		}
	}
	// 连续性：膝边界两侧必须相等。
	lo := softKneeGainDB(tDB-3, tDB, 3, 4)
	hi := softKneeGainDB(tDB+3, tDB, 3, 4)
	if math.Abs(lo) > eps || math.Abs(hi+2.25) > eps {
		t.Fatalf("膝边界不连续 %v %v", lo, hi)
	}
	// 限幅器（ratio=0）：满幅输入增益恰为 -门限dB，输出回到门限。
	lim := softKneeGainDB(0, tDB, 3, 0)
	if math.Abs(lim-tDB) > eps {
		t.Fatalf("限幅器满幅增益应为 %.6f，实得 %.6f", tDB, lim)
	}
}

func TestCompressVsReference(t *testing.T) {
	src, _ := NewClip(8000, 1600)
	if err := Sine(src, 123, 30000); err != nil {
		t.Fatal(err)
	}
	// 混入一个脉冲，制造非平稳包络。
	src.Frames[700] = 32767
	cfg := CompressorConfig{
		Threshold: 8000, Ratio: 4, KneeDB: 6,
		AttackMs: 2, ReleaseMs: 40, Enabled: true,
	}
	dst, _ := NewClip(8000, 1600)
	ref, _ := NewClip(8000, 1600)
	if _, err := Compress(dst, src, cfg); err != nil {
		t.Fatal(err)
	}
	refCompress(ref, src, cfg)

	maxAbs := 0
	for i := range dst.Frames {
		d := int(dst.Frames[i]) - int(ref.Frames[i])
		if d < 0 {
			d = -d
		}
		if d > maxAbs {
			maxAbs = d
		}
	}
	// 浮点参考与实现各自独立取整：解析上界 1 LSB（单次四舍五入本身为 0.5 LSB）。
	if maxAbs > 1 {
		t.Fatalf("逐样本峰值误差上界 1 LSB，实测 %d LSB", maxAbs)
	}
	if d := Peak(dst) - Peak(ref); d < -1 || d > 1 {
		t.Fatalf("峰值误差超出 ±1 LSB：%d", d)
	}
	t.Logf("逐样本最大误差 = %d LSB（上界 1 LSB），实现峰值 %d，参考峰值 %d",
		maxAbs, Peak(dst), Peak(ref))
}

func TestCompressDisabledBitExact(t *testing.T) {
	src, _ := NewClip(8000, 300)
	if err := Sine(src, 200, 12345); err != nil {
		t.Fatal(err)
	}
	dst, _ := NewClip(8000, 300)
	for i := range dst.Frames {
		dst.Frames[i] = -1 // 预置脏数据，确认被逐样本覆盖
	}
	// 未启用时即使参数非法也不校验、不报错。
	cfg := CompressorConfig{Threshold: 0, Ratio: -1, Enabled: false}
	rep, err := Compress(dst, src, cfg)
	if err != nil {
		t.Fatalf("未启用不应报错：%v", err)
	}
	for i := range src.Frames {
		if dst.Frames[i] != src.Frames[i] {
			t.Fatalf("第 %d 帧被改动：%d -> %d", i, src.Frames[i], dst.Frames[i])
		}
	}
	if rep.InputPeak != rep.OutputPeak {
		t.Fatal("未启用时峰值必须不变")
	}
	// 未启用但结构性不匹配：返回错误而非越界 panic。
	bad, _ := NewClip(8000, 100)
	if _, err := Compress(bad, src, CompressorConfig{Enabled: false}); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("帧数不匹配应返回 ErrSizeMismatch，实得 %v", err)
	}
	badRate, _ := NewClip(44100, 300)
	if _, err := Compress(badRate, src, CompressorConfig{Enabled: false}); !errors.Is(err, ErrRateMismatch) {
		t.Fatalf("采样率不匹配应返回 ErrRateMismatch，实得 %v", err)
	}
}

func TestCompressSine(t *testing.T) {
	src, _ := NewClip(8000, 800)
	if err := Sine(src, 100, 30000); err != nil {
		t.Fatal(err)
	}
	dst, _ := NewClip(8000, 800)
	cfg := CompressorConfig{
		Threshold: 10000, Ratio: 4, KneeDB: 6,
		AttackMs: 5, ReleaseMs: 50, Enabled: true,
	}
	rep, err := Compress(dst, src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OutputPeak >= rep.InputPeak {
		t.Fatalf("正弦应被压下：入峰 %d 出峰 %d", rep.InputPeak, rep.OutputPeak)
	}
	if rep.MaxGainReduce <= 0 {
		t.Fatal("应存在增益衰减")
	}
}

func TestCompressPulse(t *testing.T) {
	src, _ := NewClip(8000, 400)
	src.Frames[50] = 32767
	dst, _ := NewClip(8000, 400)
	cfg := CompressorConfig{
		Threshold: 4000, Ratio: 0, KneeDB: 6, // 限幅器
		AttackMs: 1, ReleaseMs: 20, Enabled: true,
	}
	rep, err := Compress(dst, src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OutputPeak >= rep.InputPeak {
		t.Fatalf("脉冲应被限幅：入峰 %d 出峰 %d", rep.InputPeak, rep.OutputPeak)
	}
	// 静音区不得被抬高（压缩器只衰减）。
	for i := 0; i < 40; i++ {
		if dst.Frames[i] != 0 {
			t.Fatalf("脉冲前静音区第 %d 帧非零：%d", i, dst.Frames[i])
		}
	}

	// 瞬时攻击（AttackMs=0，lookahead=0）的硬限幅：单样本脉冲也必须被压下。
	pls2, _ := NewClip(8000, 400)
	pls2.Frames[50] = 32767
	dst2, _ := NewClip(8000, 400)
	instantCfg := cfg
	instantCfg.AttackMs = 0
	rep2, err := Compress(dst2, pls2, instantCfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep2.OutputPeak >= 32767 || dst2.Frames[50] == 32767 {
		t.Fatalf("瞬时攻击应限住单样本脉冲：出峰 %d 脉冲帧 %d",
			rep2.OutputPeak, dst2.Frames[50])
	}
	t.Logf("瞬时限幅：脉冲帧 32767 -> %d", dst2.Frames[50])
}

func TestCompressSilence(t *testing.T) {
	src, _ := NewClip(8000, 500)
	dst, _ := NewClip(8000, 500)
	for i := range dst.Frames {
		dst.Frames[i] = 123
	}
	cfg := CompressorConfig{
		Threshold: 1000, Ratio: 2, KneeDB: 6,
		AttackMs: 5, ReleaseMs: 50, Enabled: true,
	}
	rep, err := Compress(dst, src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range dst.Frames {
		if v != 0 {
			t.Fatalf("静音输入第 %d 帧输出非零：%d", i, v)
		}
	}
	if rep.MaxGainReduce != 0 || rep.OutputPeak != 0 {
		t.Fatalf("静音不应有衰减：reduce=%v peak=%d", rep.MaxGainReduce, rep.OutputPeak)
	}
}

func TestCompressFullScale(t *testing.T) {
	src, _ := NewClip(8000, 2000)
	for i := range src.Frames {
		src.Frames[i] = 32767
	}
	dst, _ := NewClip(8000, 2000)
	// 限幅器、门限 16384：稳态输出应收敛到门限附近。
	cfg := CompressorConfig{
		Threshold: 16384, Ratio: 0, KneeDB: 6,
		AttackMs: 2, ReleaseMs: 2, Enabled: true,
	}
	rep, err := Compress(dst, src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OutputPeak > rep.InputPeak {
		t.Fatal("任何情况下输出峰值不得超过输入")
	}
	tail := dst.Frames[1500]
	if got := int(tail); got < 15000 || got > 17500 {
		t.Fatalf("满幅限幅稳态应收敛到门限 16384 附近，实得 %d", got)
	}
}

func TestCompressErrorCodes(t *testing.T) {
	src, _ := NewClip(8000, 100)
	dst, _ := NewClip(8000, 100)

	// 门限为零 -> ErrThreshold，错误信息指明门限。
	if _, err := Compress(dst, src, CompressorConfig{
		Threshold: 0, Ratio: 2, Enabled: true,
	}); !errors.Is(err, ErrThreshold) {
		t.Fatalf("门限为零应返回 ErrThreshold，实得 %v", err)
	}
	// 比率为负 -> ErrRatio，错误信息指明比率。
	if _, err := Compress(dst, src, CompressorConfig{
		Threshold: 1000, Ratio: -1, Enabled: true,
	}); !errors.Is(err, ErrRatio) {
		t.Fatalf("比率为负应返回 ErrRatio，实得 %v", err)
	}
	// 时长不足（帧数 <= 攻击+释放帧数）-> ErrDuration。
	if _, err := Compress(dst, src, CompressorConfig{
		Threshold: 1000, Ratio: 2, AttackMs: 10, ReleaseMs: 10, Enabled: true,
	}); !errors.Is(err, ErrDuration) {
		t.Fatalf("时长不足应返回 ErrDuration，实得 %v", err)
	}
	// 极短片段（1 帧）同样 -> ErrDuration。
	tiny, _ := NewClip(8000, 1)
	tinyDst, _ := NewClip(8000, 1)
	if _, err := Compress(tinyDst, tiny, CompressorConfig{
		Threshold: 1000, Ratio: 2, Enabled: true,
	}); !errors.Is(err, ErrDuration) {
		t.Fatalf("1 帧极短片段应返回 ErrDuration，实得 %v", err)
	}
}
