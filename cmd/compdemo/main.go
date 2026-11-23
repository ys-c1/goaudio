// compdemo：软拐点压缩器演示与计时程序。
//
// 输出（stdout + artifacts/compress_report.html）：
//   - 压缩前后各一张静态增益曲线（门限/比率逐个列值的表 + SVG 折线）
//   - 两档时长（短 0.1s / 长 1.0s）各跑两次，取均值，报压缩量与耗时
//   - 峰值与增益汇总表
//   - 极短片段（1 帧）单独说明并计时两次（两次取均值）
//   - 静音、脉冲、满幅、未启用逐样本校验
package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"goaudio/audio"
)

const rate = 8000

func main() {
	outDir := "artifacts"
	if len(os.Args) > 1 {
		outDir = os.Args[1]
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fatal(err)
	}

	var sb strings.Builder
	w := func(format string, a ...any) {
		if format == "" {
			sb.WriteByte('\n')
			return
		}
		fmt.Fprintf(&sb, format+"\n", a...)
	}

	w("================ 软拐点压缩器演示 ================")
	w("")

	// ---------- 1. 压缩前后两张静态增益曲线 ----------
	// “压缩前”：未启用的直通配置（增益恒为 0 dB）；“压缩后”：压缩配置。
	curveCfg := audio.CompressorConfig{
		Threshold: 8192, Ratio: 4, KneeDB: 6,
		AttackMs: 5, ReleaseMs: 50, Enabled: true,
	}
	points := make([]float64, 0, 25)
	for db := -48.0; db <= 0.0; db += 2 {
		points = append(points, db)
	}
	w("【曲线 A · 压缩前（未启用，直通）】门限/比率不参与，增益恒为 0.00 dB")
	w("  输入(dBFS)  增益(dB)")
	before := make([][2]float64, len(points))
	for i, db := range points {
		before[i] = [2]float64{db, 0}
		w("  %9.1f  %8.2f", db, 0.0)
	}
	w("")
	w("【曲线 B · 压缩后】门限=%d (%.2f dBFS)  比率=%.1f:1  膝宽=%.1f dB",
		curveCfg.Threshold, ampDB(curveCfg.Threshold), curveCfg.Ratio, curveCfg.KneeDB)
	w("  输入(dBFS)  增益(dB)   说明")
	after := audio.StaticGainCurve(points, curveCfg)
	thrDB := ampDB(curveCfg.Threshold)
	for _, p := range after {
		note := "不压缩"
		if math.Abs(p[0]-thrDB) < 1.01 {
			note = "门限附近（软拐点内）"
		} else if p[0] > thrrDB(curveCfg)+curveCfg.KneeDB/2 {
			note = "全比率压缩"
		} else if p[1] < 0 {
			note = "软拐点"
		}
		w("  %9.1f  %8.3f   %s", p[0], p[1], note)
	}
	w("")

	// ---------- 2. 两档时长，各跑两次取均值 ----------
	type tier struct {
		name   string
		frames int
	}
	tiers := []tier{
		{"短片段 0.10s", rate / 10},
		{"长片段 1.00s", rate},
	}
	cfg := audio.CompressorConfig{
		Threshold: 10000, Ratio: 4, KneeDB: 6,
		AttackMs: 5, ReleaseMs: 50, Enabled: true,
	}
	w("【两档时长 · 各测量两次取均值】门限=%d 比率=%.1f:1 膝宽=%.1fdB 攻击=%.0fms 释放=%.0fms",
		cfg.Threshold, cfg.Ratio, cfg.KneeDB, cfg.AttackMs, cfg.ReleaseMs)
	w("%-14s %8s %10s %10s %12s %12s %12s",
		"档位", "帧数", "入峰", "出峰", "压缩量(dB)", "两次耗时", "均值耗时")
	type tierResult struct {
		t                     tier
		inPeak, outPeak       int
		reduceDB, meanElapsed float64
		runs                  [2]time.Duration
	}
	tierResults := make([]tierResult, 0, len(tiers))
	for _, t := range tiers {
		src, err := audio.NewClip(rate, t.frames)
		if err != nil {
			fatal(err)
		}
		if err := audio.Sine(src, 1000, 30000); err != nil {
			fatal(err)
		}
		var runs [2]time.Duration
		var rep *audio.CompressReport
		for k := 0; k < 2; k++ {
			dst, _ := audio.NewClip(rate, t.frames)
			r, err := audio.Compress(dst, src, cfg)
			if err != nil {
				fatal(err)
			}
			runs[k] = r.Elapsed
			rep = r
			if k == 1 {
				// 保存长档压缩前后 WAV（短档同样保存，便于对照）。
				_ = src.SaveWAV(filepath.Join(outDir, fmt.Sprintf("%s_before.wav", slug(t.name))))
				_ = dst.SaveWAV(filepath.Join(outDir, fmt.Sprintf("%s_after.wav", slug(t.name))))
			}
		}
		meanNS := float64(runs[0].Nanoseconds()+runs[1].Nanoseconds()) / 2
		meanElapsed := time.Duration(meanNS)
		tierResults = append(tierResults, tierResult{
			t: t, inPeak: rep.InputPeak, outPeak: rep.OutputPeak,
			reduceDB: rep.MaxGainReduce, meanElapsed: float64(meanElapsed), runs: runs,
		})
		w("%-14s %8d %10d %10d %12.3f %12s %12s",
			t.name, t.frames, rep.InputPeak, rep.OutputPeak, rep.MaxGainReduce,
			fmt.Sprintf("%s/%s", runs[0].Round(time.Microsecond), runs[1].Round(time.Microsecond)),
			meanElapsed.Round(time.Microsecond))
	}
	w("  注：两次测量取均值以减少抖动；压缩量取全片段最大增益衰减。")
	w("")

	// ---------- 3. 峰值与增益汇总表（多组门限/比率逐个列值）----------
	w("【峰值与增益表 · 门限与比率逐个列值】信号：1kHz 正弦，峰值 30000，4000 帧")
	src, _ := audio.NewClip(rate, 4000)
	if err := audio.Sine(src, 1000, 30000); err != nil {
		fatal(err)
	}
	type row struct {
		thr int
		r   float64
		rep *audio.CompressReport
	}
	thresholds := []int{4096, 8192, 16384, 24576}
	ratios := []float64{2, 4, 8, 0} // 0 = ∞:1 限幅
	w("%8s %10s %10s %10s %14s %12s", "门限", "门限dB", "比率", "入峰", "出峰", "最大衰减dB")
	var rows []row
	for _, thr := range thresholds {
		for _, r := range ratios {
			c := audio.CompressorConfig{
				Threshold: thr, Ratio: r, KneeDB: 6,
				AttackMs: 5, ReleaseMs: 50, Enabled: true,
			}
			dst, _ := audio.NewClip(rate, 4000)
			rep, err := audio.Compress(dst, src, c)
			if err != nil {
				fatal(err)
			}
			ratioLabel := "∞:1"
			if r > 0 {
				ratioLabel = fmt.Sprintf("%.0f:1", r)
			}
			w("%8d %10.2f %10s %10d %14d %12.3f",
				thr, ampDB(thr), ratioLabel, rep.InputPeak, rep.OutputPeak, rep.MaxGainReduce)
			rows = append(rows, row{thr, r, rep})
		}
	}
	w("")

	// ---------- 4. 极短片段：单独说明并计时两次 ----------
	w("【极短片段 · 1 帧】")
	tiny, err := audio.NewClip(rate, 1)
	if err != nil {
		fatal(err)
	}
	tiny.Frames[0] = 32767
	tinyCfg := audio.CompressorConfig{
		Threshold: 1000, Ratio: 4, KneeDB: 6,
		AttackMs: 5, ReleaseMs: 50, Enabled: true,
	}
	var tinyRuns [2]time.Duration
	var tinyErr error
	for k := 0; k < 2; k++ {
		dst, _ := audio.NewClip(rate, 1)
		start := time.Now()
		_, tinyErr = audio.Compress(dst, tiny, tinyCfg)
		tinyRuns[k] = time.Since(start)
	}
	tinyMean := time.Duration(float64(tinyRuns[0].Nanoseconds()+tinyRuns[1].Nanoseconds()) / 2)
	w("  说明：包络跟随器需要攻击+释放时间常数，1 帧无法承载，压缩器拒绝处理并返回错误码。")
	w("  返回错误：%v", tinyErr)
	w("  errors.Is(ErrDuration) = %v", errors.Is(tinyErr, audio.ErrDuration))
	w("  第 1 次计时 %s，第 2 次 %s，两次均值 %s（两次取平均以减少抖动）",
		tinyRuns[0].Round(time.Nanosecond), tinyRuns[1].Round(time.Nanosecond), tinyMean.Round(time.Nanosecond))
	w("")

	// ---------- 5. 边界信号与未启用逐样本校验 ----------
	w("【边界信号】")
	// 静音
	sil, _ := audio.NewClip(rate, 500)
	silDst, _ := audio.NewClip(rate, 500)
	silRep, err := audio.Compress(silDst, sil, cfg)
	if err != nil {
		fatal(err)
	}
	w("  静音 500 帧：出峰=%d 最大衰减=%.3f dB（要求出峰 0）", silRep.OutputPeak, silRep.MaxGainReduce)
	// 脉冲
	pls, _ := audio.NewClip(rate, 400)
	pls.Frames[50] = 32767
	plsDst, _ := audio.NewClip(rate, 400)
	limCfg := cfg
	limCfg.Ratio = 0
	limCfg.AttackMs = 0 // 瞬时攻击：单样本瞬态也能被限住（lookahead=0）
	limCfg.ReleaseMs = 20
	plsRep, err := audio.Compress(plsDst, pls, limCfg)
	if err != nil {
		fatal(err)
	}
	w("  单脉冲（限幅器）：入峰=%d 出峰=%d", plsRep.InputPeak, plsRep.OutputPeak)
	// 满幅
	fs, _ := audio.NewClip(rate, 2000)
	for i := range fs.Frames {
		fs.Frames[i] = 32767
	}
	fsDst, _ := audio.NewClip(rate, 2000)
	fsRep, err := audio.Compress(fsDst, fs, limCfg)
	if err != nil {
		fatal(err)
	}
	w("  满幅直流（限幅器）：入峰=%d 出峰=%d 稳态尾帧=%d（门限 10000 附近）",
		fsRep.InputPeak, fsRep.OutputPeak, fsDst.Frames[1500])
	// 未启用：逐样本不变
	enSrc, _ := audio.NewClip(rate, 600)
	if err := audio.Sine(enSrc, 440, 12345); err != nil {
		fatal(err)
	}
	enDst, _ := audio.NewClip(rate, 600)
	offCfg := audio.CompressorConfig{Enabled: false}
	if _, err := audio.Compress(enDst, enSrc, offCfg); err != nil {
		fatal(err)
	}
	exact := true
	for i := range enSrc.Frames {
		if enDst.Frames[i] != enSrc.Frames[i] {
			exact = false
			break
		}
	}
	w("  未启用（Enabled=false）：逐样本输出不变 = %v", exact)
	// 错误码点名
	_, e1 := audio.Compress(mustClip(rate, 100), mustClip(rate, 100),
		audio.CompressorConfig{Threshold: 0, Ratio: 2, Enabled: true})
	_, e2 := audio.Compress(mustClip(rate, 100), mustClip(rate, 100),
		audio.CompressorConfig{Threshold: 1000, Ratio: -3, Enabled: true})
	w("  门限为零 -> %v", e1)
	w("  比率为负 -> %v", e2)
	w("")

	// ---------- HTML 报告 ----------
	htmlPath := filepath.Join(outDir, "compress_report.html")
	if err := os.WriteFile(htmlPath, []byte(renderHTML(before, after, curveCfg)), 0o644); err != nil {
		fatal(err)
	}
	w("HTML 报告（含两条增益曲线 SVG）：%s", htmlPath)

	fmt.Print(sb.String())
}

func mustClip(rate, n int) *audio.Clip {
	c, err := audio.NewClip(rate, n)
	if err != nil {
		fatal(err)
	}
	return c
}

func ampDB(amp int) float64 {
	return 20 * math.Log10(float64(amp)/32768.0)
}

// thrrDB 返回配置门限的 dBFS，仅用于注释判定。
func thrrDB(c audio.CompressorConfig) float64 { return ampDB(c.Threshold) }

func slug(s string) string {
	r := strings.NewReplacer(" ", "_", ".", "_")
	return r.Replace(s)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "错误：", err)
	os.Exit(1)
}
