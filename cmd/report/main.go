// 报告生成器：构造两档时长（1 s / 10 s）的相位错开素材，
// 分别做硬切与三档交叉淡化，逐样本核验接缝不变量，输出 Markdown 报告与 CSV。
//
// 用法：go run ./cmd/report [-out artifacts] [-runs 50]
// 任一真实淡化的最大跳变超过 MaxJump 阈值时以非零状态退出。
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"goaudio/audio"
)

const (
	rate      = 8000
	amp       = 10000
	freqA     = 440.0
	freqB     = 660.0
	threshold = 7000
)

type fadeSpec struct {
	label string
	ms    int
	frames int
}

type durationSpec struct {
	label  string
	frames int
}

type runResult struct {
	ds       durationSpec
	fs       fadeSpec
	hard     bool
	out      *audio.Clip
	jumps    []audio.Jump
	maxJump  int
	maxAt    int
	seam     []audio.SeamSample
	median   time.Duration
	overlap  int
	fadeRows []fadeRow
}

type fadeRow struct {
	index                       int
	phase, wa, wb               float64
	a, b                        int16
	waA, wbB, sum               float64
	out                         int16
	err                         int
	jump                        int
}

func main() {
	outDir := flag.String("out", "artifacts", "报告与 CSV 输出目录")
	runs := flag.Int("runs", 50, "耗时测量的完整调用次数（取中位数）")
	flag.Parse()

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fail("创建输出目录失败: %v", err)
	}

	durations := []durationSpec{
		{label: "1 s", frames: rate},
		{label: "10 s", frames: 10 * rate},
	}
	fades := []fadeSpec{
		{label: "短淡 10ms", ms: 10, frames: 80},
		{label: "中淡 50ms", ms: 50, frames: 400},
		{label: "长淡 100ms", ms: 100, frames: 800},
	}

	var results []*runResult
	thresholdFail := false

	for _, ds := range durations {
		a, b := buildPair(ds.frames)

		// 硬切对照。
		hardOut, hardJumps, hardSeam, hardMedian := runHard(a, b, *runs)
		hardMax, hardAt := audio.MaxJump(hardJumps)
		results = append(results, &runResult{
			ds: ds, hard: true, out: hardOut, jumps: hardJumps,
			maxJump: hardMax, maxAt: hardAt, seam: hardSeam, median: hardMedian,
		})

		for _, fs := range fades {
			r, err := runFade(a, b, ds, fs, *runs)
			if err != nil {
				fail("交叉淡化失败: %v", err)
			}
			// 不变量核验：接缝四样本 Diff 必须为 0；淡化区逐样本 Err 必须为 0。
			for _, s := range r.seam {
				if s.Diff != 0 {
					fail("接缝不变量失效 %s %s: %+v", ds.label, fs.label, s)
				}
			}
			for _, row := range r.fadeRows {
				if row.err != 0 {
					fail("淡化区混合误差非零 %s %s 下标 %d: err=%d", ds.label, fs.label, row.index, row.err)
				}
				if row.index > r.ds.frames-r.overlap && math.Abs(row.wa*row.wa+row.wb*row.wb-1) > 1e-12 {
					fail("权重恒等式失效 %s %s 下标 %d", ds.label, fs.label, row.index)
				}
			}
			if err := audio.CheckMaxJump(r.jumps, threshold); err != nil {
				thresholdFail = true
				fmt.Fprintf(os.Stderr, "阈值判定失败 %s %s: %v\n", ds.label, fs.label, err)
			}
			results = append(results, r)
		}
	}

	if err := writeReport(filepath.Join(*outDir, "xfade-report.md"), results, *runs); err != nil {
		fail("写报告失败: %v", err)
	}
	if err := writeCSVs(*outDir, results); err != nil {
		fail("写 CSV 失败: %v", err)
	}

	fmt.Printf("报告已写入 %s\n", filepath.Join(*outDir, "xfade-report.md"))
	if thresholdFail {
		os.Exit(1)
	}
}

func buildPair(frames int) (a, b *audio.Clip) {
	var err error
	if a, err = audio.NewClip(rate, frames); err != nil {
		fail("建 Clip 失败: %v", err)
	}
	if b, err = audio.NewClip(rate, frames); err != nil {
		fail("建 Clip 失败: %v", err)
	}
	if err = audio.Sine(a, freqA, amp); err != nil {
		fail("合成 A 失败: %v", err)
	}
	if err = audio.SinePhase(b, freqB, amp, math.Pi/2); err != nil {
		fail("合成 B 失败: %v", err)
	}
	return a, b
}

func medianDuration(ds []time.Duration) time.Duration {
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}

func runHard(a, b *audio.Clip, runs int) (*audio.Clip, []audio.Jump, []audio.SeamSample, time.Duration) {
	timings := make([]time.Duration, runs)
	var out *audio.Clip
	for i := 0; i < runs; i++ {
		t0 := time.Now()
		var err error
		out, err = audio.HardCut(a, b)
		timings[i] = time.Since(t0)
		if err != nil {
			fail("硬切失败: %v", err)
		}
	}
	jumps, err := audio.SeamJumps(a, out, 0)
	if err != nil {
		fail("硬切跳变统计失败: %v", err)
	}
	return out, jumps, nil, medianDuration(timings)
}

func runFade(a, b *audio.Clip, ds durationSpec, fs fadeSpec, runs int) (*runResult, error) {
	timings := make([]time.Duration, runs)
	var out *audio.Clip
	var err error
	for i := 0; i < runs; i++ {
		t0 := time.Now()
		out, err = audio.XFade(a, b, fs.frames)
		timings[i] = time.Since(t0)
		if err != nil {
			return nil, err
		}
	}
	jumps, err := audio.SeamJumps(a, out, fs.frames)
	if err != nil {
		return nil, err
	}
	maxJump, maxAt := audio.MaxJump(jumps)
	seam := audio.SeamSamples(a, b, out, fs.frames)

	// 淡化区逐样本：相位、权重、两侧贡献、混合误差、该样本处跳变。
	rows := make([]fadeRow, 0, fs.frames)
	base := ds.frames - fs.frames
	for i := 0; i < fs.frames; i++ {
		idx := base + i
		var phase float64
		if fs.frames > 1 {
			phase = math.Pi / 2 * float64(i) / float64(fs.frames-1)
		} else {
			phase = math.Pi / 4
		}
		wa, wb := arcWeights(i, fs.frames)
		av, bv := a.Frames[idx], b.Frames[i]
		waA, wbB := wa*float64(av), wb*float64(bv)
		sum := waA + wbB
		want := int16(math.Round(sum))
		if want > math.MaxInt16 {
			want = math.MaxInt16
		} else if want < math.MinInt16 {
			want = math.MinInt16
		}
		j := 0
		if idx+1 < len(out.Frames) {
			j = absInt(int(out.Frames[idx+1]) - int(out.Frames[idx]))
		}
		rows = append(rows, fadeRow{
			index: idx, phase: phase, wa: wa, wb: wb, a: av, b: bv,
			waA: waA, wbB: wbB, sum: sum, out: out.Frames[idx],
			err: int(out.Frames[idx]) - int(want), jump: j,
		})
	}

	return &runResult{
		ds: ds, fs: fs, out: out, jumps: jumps, maxJump: maxJump, maxAt: maxAt,
		seam: seam, median: medianDuration(timings), overlap: fs.frames, fadeRows: rows,
	}, nil
}

// arcWeights 与 audio 包内部实现保持同构，用于报告侧独立复算。
func arcWeights(i, n int) (wa, wb float64) {
	if n <= 1 {
		w := math.Sqrt(0.5)
		return w, w
	}
	if i == 0 {
		return 1, 0
	}
	if i == n-1 {
		return 0, 1
	}
	phase := math.Pi / 2 * float64(i) / float64(n-1)
	return math.Cos(phase), math.Sin(phase)
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func durTag(ds durationSpec) string {
	if ds.frames == rate {
		return "1s"
	}
	return "10s"
}

func fadeTag(fs fadeSpec) string {
	return fmt.Sprintf("%dms", fs.ms)
}

// ---------- Markdown 报告 ----------

func writeReport(path string, results []*runResult, runs int) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# 相位对齐交叉淡化 — 接缝跳变核验报告\n\n")
	fmt.Fprintf(&sb, "- 采样率 %d Hz，A=%g Hz，B=%g Hz 且相位错开 π/2（硬切处故意制造咔哒声）\n", rate, freqA, freqB)
	fmt.Fprintf(&sb, "- 淡化：相位按单位圆第一象限弧长均分 `phase(i)=π/2·i/(n−1)`，权重 `wa=cos(phase)`、`wb=sin(phase)`；两端点精确钉为 (1,0)/(0,1)\n")
	fmt.Fprintf(&sb, "- 单样本淡化（n=1）取角平分点 π/4，两侧等权 √2/2，仍满足 wa²+wb²=1\n")
	fmt.Fprintf(&sb, "- 接缝跳变阈值 `MaxJump=%d`（只对真实淡化判定；硬切为对照，如实列出其超标跳变，不计入失败）；耗时取 %d 次完整调用的中位数\n", threshold, runs)
	fmt.Fprintf(&sb, "- 不变量：接缝两侧四个样本与源素材逐位相等（Diff=0）；淡化区每样本 `Out=round(wa·A+wb·B)`（Err=0）\n\n")

	// 表 1
	sb.WriteString("## 表 1　两档素材时长：接缝跳变与耗时\n\n")
	sb.WriteString("| 素材时长 | 处理 | 重叠帧 | 接缝样本差 | 接缝区最大跳变 | 峰值样本 | 阈值判定 | 耗时(中位) |\n")
	sb.WriteString("|---:|:---|---:|:---:|---:|---:|:---:|---:|\n")
	for _, r := range results {
		process, diffCell, verdict, overlap := table1Cells(r)
		fmt.Fprintf(&sb, "| %s | %s | %d | %s | %d | %d | %s | %s |\n",
			r.ds.label, process, overlap, diffCell, r.maxJump, r.maxAt, verdict, r.median)
	}

	// 表 2
	sb.WriteString("\n## 表 2　接缝两侧样本差（逐样本，须全为 0）\n\n")
	sb.WriteString("| 素材时长 | 处理 | 接缝样本 | 输出下标 | 输出样本 | 源样本 | Diff |\n")
	sb.WriteString("|---:|:---|:---|---:|---:|---:|---:|\n")
	for _, r := range results {
		if r.hard {
			continue
		}
		for _, s := range r.seam {
			fmt.Fprintf(&sb, "| %s | %s | %s | %d | %d | %d | %d |\n",
				r.ds.label, r.fs.label, s.Name, s.OutIndex, s.Out, s.Src, s.Diff)
		}
	}

	// 表 3
	sb.WriteString("\n## 表 3　接缝跳变逐样本（相邻样本对逐点列出）\n\n")
	sb.WriteString("硬切列出拼接点一对；短/中/长淡覆盖入口跨越对、淡化区内部各对、出口跨越对，分别为 81/401/801 对。\n")
	sb.WriteString("每一对都给出左右样本与实际绝对差；最大跳变所在行加粗标记。\n\n")
	for _, r := range results {
		process := "硬切"
		if !r.hard {
			process = r.fs.label
		}
		fmt.Fprintf(&sb, "### %s 素材 · %s（最大跳变 %d @样本 %d）\n\n",
			r.ds.label, process, r.maxJump, r.maxAt)
		sb.WriteString("| 左样本下标 | 左样本 | 右样本 | |右−左| |\n")
		sb.WriteString("|---:|---:|---:|---:|\n")
		for _, j := range r.jumps {
			if j.AbsDiff == r.maxJump {
				fmt.Fprintf(&sb, "| **%d** | **%d** | **%d** | **%d** |\n",
					j.LeftIndex, j.Left, j.Right, j.AbsDiff)
			} else {
				fmt.Fprintf(&sb, "| %d | %d | %d | %d |\n",
					j.LeftIndex, j.Left, j.Right, j.AbsDiff)
			}
		}
		sb.WriteByte('\n')
	}

	sb.WriteString("## CSV\n\n")
	sb.WriteString("- `fade-samples-<时长>s-<淡化>.csv`：淡化区逐样本（下标、相位、wa、wb、A、B、wa·A、wb·B、和、输出、误差、该样本处跳变）\n")
	sb.WriteString("- `seam-jumps-<时长>s-<淡化>.csv`：接缝区逐样本跳变（左下标、左样本、右样本、|右−左|）\n")
	sb.WriteString("- `seam-jumps-<时长>s-all.csv`：该时长下全部处理（含硬切）的跳变汇总，首列 `fade` 标识处理档\n")
	sb.WriteString("- 权重恒等式：非端点样本 `wa²+wb²=1`；端点精确 `(1,0)/(0,1)`\n")

	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

func table1Cells(r *runResult) (process, diffCell, verdict string, overlap int) {
	if r.hard {
		overlap = 0
		diffCell = "—"
		if r.maxJump > threshold {
			verdict = "对照（超阈值）"
		} else {
			verdict = "对照"
		}
		return "硬切", diffCell, verdict, overlap
	}
	overlap = r.overlap
	zero := true
	for _, s := range r.seam {
		if s.Diff != 0 {
			zero = false
		}
	}
	if zero {
		diffCell = fmt.Sprintf("%d 个样本均 0", len(r.seam))
	} else {
		diffCell = "存在非零差"
	}
	if r.maxJump > threshold {
		verdict = "失败（超阈值）"
	} else {
		verdict = "通过"
	}
	return r.fs.label, diffCell, verdict, overlap
}

// ---------- CSV ----------

func writeCSVs(dir string, results []*runResult) error {
	for _, r := range results {
		if !r.hard {
			path := filepath.Join(dir, fmt.Sprintf("fade-samples-%s-%s.csv", durTag(r.ds), fadeTag(r.fs)))
			if err := writeFadeCSV(path, r); err != nil {
				return err
			}
			// 兼容旧命名：短淡同时写入 fade-samples-<时长>s.csv。
			if r.fs.ms == 10 && (r.ds.frames == rate || r.ds.frames == 10*rate) {
				legacy := filepath.Join(dir, fmt.Sprintf("fade-samples-%s.csv", durTag(r.ds)))
				if err := writeFadeCSV(legacy, r); err != nil {
					return err
				}
			}
		}
		path := filepath.Join(dir, fmt.Sprintf("seam-jumps-%s-%s.csv",
			durTag(r.ds), map[bool]string{true: "hard", false: fadeTag(r.fs)}[r.hard]))
		if err := writeJumpCSV(path, r, false); err != nil {
			return err
		}
	}
	// 每档时长一份汇总。
	for _, ds := range []durationSpec{{label: "1 s", frames: rate}, {label: "10 s", frames: 10 * rate}} {
		path := filepath.Join(dir, fmt.Sprintf("seam-jumps-%s-all.csv", durTag(ds)))
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		fmt.Fprintln(f, "fade,left_index,left,right,abs_diff")
		for _, r := range results {
			if r.ds.frames != ds.frames {
				continue
			}
			tag := "hard"
			if !r.hard {
				tag = fadeTag(r.fs)
			}
			for _, j := range r.jumps {
				fmt.Fprintf(f, "%s,%d,%d,%d,%d\n", tag, j.LeftIndex, j.Left, j.Right, j.AbsDiff)
			}
		}
		f.Close()

		// 兼容旧命名：短淡跳变同时写入 seam-jumps-<时长>s.csv。
		for _, r := range results {
			if r.ds.frames == ds.frames && !r.hard && r.fs.ms == 10 {
				legacy := filepath.Join(dir, fmt.Sprintf("seam-jumps-%s.csv", durTag(ds)))
				if err := writeJumpCSV(legacy, r, false); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func writeJumpCSV(path string, r *runResult, withFadeCol bool) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if withFadeCol {
		fmt.Fprintln(f, "fade,left_index,left,right,abs_diff")
	} else {
		fmt.Fprintln(f, "left_index,left,right,abs_diff")
	}
	for _, j := range r.jumps {
		if withFadeCol {
			tag := "hard"
			if !r.hard {
				tag = fadeTag(r.fs)
			}
			fmt.Fprintf(f, "%s,%d,%d,%d,%d\n", tag, j.LeftIndex, j.Left, j.Right, j.AbsDiff)
		} else {
			fmt.Fprintf(f, "%d,%d,%d,%d\n", j.LeftIndex, j.Left, j.Right, j.AbsDiff)
		}
	}
	return nil
}

func writeFadeCSV(path string, r *runResult) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintln(f, "index,phase,wa,wb,A,B,wa*A,wb*B,sum,out,err,jump")
	for _, x := range r.fadeRows {
		fmt.Fprintf(f, "%d,%.15f,%.15f,%.15f,%d,%d,%.6f,%.6f,%.6f,%d,%d,%d\n",
			x.index, x.phase, x.wa, x.wb, x.a, x.b, x.waA, x.wbB, x.sum, x.out, x.err, x.jump)
	}
	return nil
}
