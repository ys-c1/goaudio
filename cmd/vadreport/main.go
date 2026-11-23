// Command vadreport 生成端点检测验收报告。
//
// 它在内存里合成五类验收素材（单段、多段、渐弱、背景噪声、全静音），
// 逐个跑 DetectEndpoints，输出段表、段表统计、漏检率，以及
// "覆盖全部真实有声 / 不跨越两次发声之间静音" 的逐条核对结果。
// 同时把每段素材写成 WAV 落在产物目录，报告里记录命令与路径。
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"goaudio/audio"
)

type inputCase struct {
	name     string
	rate     int
	totalMs  float64
	truth    []audio.Segment
	build    func(c *audio.Clip)
	note     string
	pathHint string
}

func msN(rate int, ms float64) int { return int(math.Round(float64(rate) * ms / 1000.0)) }

func addTone(c *audio.Clip, a, b int, freq, ampStart, ampEnd float64) {
	edge := msN(c.Rate, 3)
	if edge < 1 {
		edge = 1
	}
	for i := a; i < b; i++ {
		t := float64(i) / float64(c.Rate)
		frac := float64(i-a) / float64(b-a)
		amp := ampStart + (ampEnd-ampStart)*frac
		g := 1.0
		if i-a < edge {
			g = math.Min(g, 0.5-0.5*math.Cos(math.Pi*float64(i-a)/float64(edge)))
		}
		if b-i-1 < edge {
			g = math.Min(g, 0.5-0.5*math.Cos(math.Pi*float64(b-i-1)/float64(edge)))
		}
		c.Frames[i] += int16(amp * g * math.Sin(2*math.Pi*freq*t))
	}
}

func addWhiteNoise(c *audio.Clip, amp float64, seed int64) {
	r := rand.New(rand.NewSource(seed))
	for i := range c.Frames {
		c.Frames[i] += int16((r.Float64()*2 - 1) * amp)
	}
}

func materialize(outDir string) ([]inputCase, error) {
	const rate = 8000
	seg := func(a, b float64) audio.Segment {
		return audio.Segment{Start: msN(rate, a), End: msN(rate, b)}
	}

	cs := []inputCase{
		{
			name: "单段", rate: rate, totalMs: 1500,
			truth: []audio.Segment{seg(400, 1000)},
			note:  "400–1000ms 单声道 300Hz 正弦，幅度 9000",
			build: func(c *audio.Clip) {
				addTone(c, seg(400, 1000).Start, seg(400, 1000).End, 300, 9000, 9000)
			},
		},
		{
			name: "多段", rate: rate, totalMs: 3000,
			truth: []audio.Segment{seg(300, 700), seg(1200, 1600), seg(2100, 2500)},
			note:  "3 段发声，段间静音 500ms",
			build: func(c *audio.Clip) {
				for _, s := range []struct{ a, b, f float64 }{
					{300, 700, 300}, {1200, 1600, 420}, {2100, 2500, 260},
				} {
					addTone(c, seg(s.a, s.b).Start, seg(s.a, s.b).End, s.f, 8000, 8000)
				}
			},
		},
		{
			name: "渐弱", rate: rate, totalMs: 1600,
			truth: []audio.Segment{seg(300, 1300)},
			note:  "300–1300ms，幅度由 12000 线性降到 2500",
			build: func(c *audio.Clip) {
				addTone(c, seg(300, 1300).Start, seg(300, 1300).End, 400, 12000, 2500)
			},
		},
		{
			name: "背景噪声", rate: rate, totalMs: 2200,
			truth: []audio.Segment{seg(500, 1200)},
			note:  "均匀白噪声 ±700 打底，500–1200ms 叠加 250Hz 正弦（幅度 9000）",
			build: func(c *audio.Clip) {
				addWhiteNoise(c, 700, 7)
				addTone(c, seg(500, 1200).Start, seg(500, 1200).End, 250, 9000, 9000)
			},
		},
		{
			name: "全静音", rate: rate, totalMs: 1200,
			note:  "全零样本",
			build: func(c *audio.Clip) {},
		},
	}

	for i := range cs {
		c, err := audio.NewClip(cs[i].rate, msN(cs[i].rate, cs[i].totalMs))
		if err != nil {
			return nil, err
		}
		cs[i].build(c)
		path := filepath.Join(outDir, fmt.Sprintf("vad_%02d_%s.wav", i+1, cs[i].name))
		if err := c.SaveWAV(path); err != nil {
			return nil, err
		}
		cs[i].pathHint = path
	}
	return cs, nil
}

func main() {
	outDir := flag.String("out", "artifacts/vad", "产物目录（WAV 与报告）")
	report := flag.String("report", "artifacts/vad/report.md", "报告路径")
	flag.Parse()

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if dir := filepath.Dir(*report); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	cfg := audio.DefaultEndpointConfig()
	bound, err := audio.SolveWindowBound(8000, cfg.MinSpeechMs, cfg.AllowedMissRate)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	cs, err := materialize(*outDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// 段表另存一份 CSV，便于下游直接消费。
	segCSV := filepath.Join(*outDir, "segments.csv")
	csvFile, err := os.Create(segCSV)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer csvFile.Close()
	cw := csv.NewWriter(csvFile)
	if err := cw.Write([]string{"case", "kind", "index", "start_sample", "end_sample", "start_ms", "end_ms", "duration_ms"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	type summaryRow struct {
		name       string
		speechSegs int
		silSegs    int
		speechSmp  int
		silSmp     int
		truthSmp   int
		missSmp    int
		missRate   float64
		coverOK    bool
		coverNA    bool
		crossings  int
		crossNA    bool
	}
	var summary []summaryRow

	var b strings.Builder
	fmt.Fprintf(&b, "# 端点检测验收报告\n\n")
	fmt.Fprintf(&b, "生成时间：%s\n\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "## 命令与产物\n\n")
	fmt.Fprintf(&b, "- 测试：`go test ./...`\n")
	fmt.Fprintf(&b, "- 报告生成：`go run ./cmd/vadreport -out %s -report %s`\n", *outDir, *report)
	fmt.Fprintf(&b, "- 报告：`%s`\n", *report)
	fmt.Fprintf(&b, "- 段表 CSV：`%s`\n", segCSV)
	for _, c := range cs {
		fmt.Fprintf(&b, "- 素材：`%s`\n", c.pathHint)
	}
	fmt.Fprintf(&b, "\n## 参数与反解\n\n")
	fmt.Fprintf(&b, "| 项 | 值 |\n|---|---|\n")
	fmt.Fprintf(&b, "| 采样率 | 8000 Hz |\n")
	fmt.Fprintf(&b, "| 允许漏检率 p | %.2f%% |\n", cfg.AllowedMissRate*100)
	fmt.Fprintf(&b, "| 最短真实发声 T_min | %.0f ms |\n", cfg.MinSpeechMs)
	fmt.Fprintf(&b, "| 反解窗长上限 2W ≤ p·T_min | %d 样本（%.1f ms） |\n", bound, 1000*float64(bound)/float64(cs[0].rate))
	fmt.Fprintf(&b, "| 实际窗长 | %.0f ms = %d 样本（≤ 上限） |\n", cfg.WindowMs, msN(cs[0].rate, cfg.WindowMs))
	fmt.Fprintf(&b, "| 帧移 | %.0f ms |\n", cfg.HopMs)
	fmt.Fprintf(&b, "| 短段合并阈值 | 间隔 < %.0f ms 合并；段长 < %.0f ms 丢弃 |\n", cfg.MinSilenceMs, cfg.MinSpeechMs)

	fmt.Fprintf(&b, "\n## 段表\n\n")
	for _, c := range cs {
		clip, err := audio.LoadWAV(c.pathHint)
		if err != nil {
			fmt.Fprintf(&b, "### %s\n\n读取失败：%v\n\n", c.name, err)
			continue
		}
		res, err := audio.DetectEndpoints(clip, cfg)
		if err != nil {
			fmt.Fprintf(&b, "### %s\n\n检测失败：%v\n\n", c.name, err)
			continue
		}
		chk := audio.CheckCoverage(res.Speech, c.truth)

		fmt.Fprintf(&b, "### %s\n\n%s\n\n", c.name, c.note)
		fmt.Fprintf(&b, "解出门限：噪声底 %.5f，能量低门限 %.5f，高门限 %.5f，过零率上限 %.3f；帧数 %d。\n\n",
			res.NoiseFloor, res.EnergyLow, res.EnergyHigh, res.ZCRLimit, res.FrameCount)
		fmt.Fprintf(&b, "实际窗长 %d 样本 ≤ 反解上限 %d 样本。\n\n", res.WindowSamples, res.WindowBound)

		fmt.Fprintf(&b, "| # | 类型 | 起点(样本) | 终点(样本) | 起点(ms) | 终点(ms) | 时长(ms) |\n|---|---|---|---|---|---|---|\n")
		printSpans := func(kind string, spans []audio.Segment) {
			for i, s := range spans {
				startMs := 1000 * float64(s.Start) / float64(clip.Rate)
				endMs := 1000 * float64(s.End) / float64(clip.Rate)
				durMs := 1000 * float64(s.Duration()) / float64(clip.Rate)
				fmt.Fprintf(&b, "| %d | %s | %d | %d | %.1f | %.1f | %.1f |\n",
					i+1, kind, s.Start, s.End, startMs, endMs, durMs)
				if err := cw.Write([]string{
					c.name, kind, strconv.Itoa(i + 1),
					strconv.Itoa(s.Start), strconv.Itoa(s.End),
					fmt.Sprintf("%.1f", startMs), fmt.Sprintf("%.1f", endMs), fmt.Sprintf("%.1f", durMs),
				}); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(1)
				}
			}
		}
		printSpans("有声", res.Speech)
		printSpans("静音", res.Silence)

		ss := audio.Stats(res.Speech)
		sl := audio.Stats(res.Silence)
		fmt.Fprintf(&b, "\n段表统计：有声 %d 段，总 %d 样本，最短 %d / 最长 %d / 平均 %d 样本；静音 %d 段，总 %d 样本，最短 %d / 最长 %d / 平均 %d 样本。\n",
			ss.Count, ss.TotalSamples, ss.MinSamples, ss.MaxSamples, ss.MeanSamples,
			sl.Count, sl.TotalSamples, sl.MinSamples, sl.MaxSamples, sl.MeanSamples)
		fmt.Fprintf(&b, "\n核对：真实有声 %d 样本，漏检 %d 样本，漏检率 %.4f%%（允许 %.2f%%）；",
			chk.TruthSamples, chk.MissedSamples, chk.MissRate*100, res.AllowedMissRate*100)
		coverOK := "通过"
		for _, ps := range chk.PerSpan {
			if !ps.FullyCovered {
				coverOK = "不通过"
			}
		}
		if len(chk.PerSpan) == 0 {
			coverOK = "无真实发声，不适用"
		}
		fmt.Fprintf(&b, "覆盖全部真实有声：%s；", coverOK)
		crossOK := "通过"
		if chk.Crossings != 0 {
			crossOK = "不通过"
		}
		if len(c.truth) < 2 {
			crossOK += "（真实发声不足两段，不存在发声间静音，本项不适用）"
		}
		fmt.Fprintf(&b, "不跨越发声间静音：%s（跨越 %d 个检测段）。\n",
			crossOK, chk.Crossings)
		if chk.FalseSamples > 0 {
			fmt.Fprintf(&b, "误检：检测为有声但不在真实发声内的样本 %d 个。\n", chk.FalseSamples)
		}
		row := summaryRow{
			name: c.name, speechSegs: len(res.Speech), silSegs: len(res.Silence),
			speechSmp: res.SpeechSamples(), silSmp: res.SilenceSamples(),
			truthSmp: chk.TruthSamples, missSmp: chk.MissedSamples, missRate: chk.MissRate,
			coverOK: coverOK == "通过", coverNA: len(chk.PerSpan) == 0,
			crossings: chk.Crossings, crossNA: len(c.truth) < 2,
		}
		summary = append(summary, row)
		fmt.Fprintf(&b, "\n")
	}

	cw.Flush()
	if err := cw.Error(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// 汇总表：段数、样本数、漏检率、两项核对。
	fmt.Fprintf(&b, "## 汇总\n\n")
	fmt.Fprintf(&b, "| 用例 | 有声段数 | 静音段数 | 有声样本 | 静音样本 | 真实有声样本 | 漏检样本 | 漏检率 | 覆盖全部真实有声 | 不跨越发声间静音 |\n")
	fmt.Fprintf(&b, "|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range summary {
		cover := "通过"
		if r.coverNA {
			cover = "不适用（无真实发声）"
		} else if !r.coverOK {
			cover = "不通过"
		}
		cross := "通过"
		if r.crossNA {
			cross = "不适用（真实发声不足两段）"
		} else if r.crossings != 0 {
			cross = fmt.Sprintf("不通过（%d 段跨越）", r.crossings)
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d | %.4f%% | %s | %s |\n",
			r.name, r.speechSegs, r.silSegs, r.speechSmp, r.silSmp,
			r.truthSmp, r.missSmp, r.missRate*100, cover, cross)
	}

	fmt.Fprintf(&b, "\n## 结论\n\n")
	allCover, allCross := true, true
	var totalTruth, totalMiss int
	for _, r := range summary {
		if !r.coverNA && !r.coverOK {
			allCover = false
		}
		if !r.crossNA && r.crossings != 0 {
			allCross = false
		}
		totalTruth += r.truthSmp
		totalMiss += r.missSmp
	}
	overall := 0.0
	if totalTruth > 0 {
		overall = float64(totalMiss) / float64(totalTruth)
	}
	fmt.Fprintf(&b, "- 全部用例覆盖真实有声：%s。\n", passWord(allCover))
	fmt.Fprintf(&b, "- 全部用例不跨越发声间静音：%s。\n", passWord(allCross))
	fmt.Fprintf(&b, "- 合计真实有声 %d 样本，漏检 %d 样本，总漏检率 %.4f%%（允许 %.2f%%）：%s。\n",
		totalTruth, totalMiss, overall*100, cfg.AllowedMissRate*100,
		passWord(overall <= cfg.AllowedMissRate))
	fmt.Fprintf(&b, "- 实际窗长 %d 样本，未超过由 p 反解的上限 %d 样本。\n", msN(cs[0].rate, cfg.WindowMs), bound)

	if err := os.WriteFile(*report, []byte(b.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("报告已写入 %s\n", *report)
	fmt.Printf("段表已写入 %s\n", segCSV)
	fmt.Printf("素材已写入 %s\n", *outDir)
}

func passWord(ok bool) string {
	if ok {
		return "通过"
	}
	return "不通过"
}
