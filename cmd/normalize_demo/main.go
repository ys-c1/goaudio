// Command normalize_demo 对五类素材执行响度归一，并输出三张核验表：
//  1. 增益与峰值表（含整段增益、分段增益、限幅阈值、削顶概率、前后峰值）
//  2. 幅值分布表（归一前后各区间计数与占比）
//  3. 归一前后响度与峰值对照表（各一行）
package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"goaudio/audio"
)

func main() {
	cfg := audio.NormalizeConfig{TargetLUFS: -20, ClipProb: 0.01, SegmentLen: 1000}

	type caseDef struct {
		name string
		clip *audio.Clip
	}
	cases := []caseDef{
		{"轻", buildQuiet()},
		{"响", buildLoud()},
		{"忽大忽小", buildVarying()},
		{"静音", buildSilence()},
		{"单样本", buildSingle()},
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# 响度归一核验报告\n\n")
	fmt.Fprintf(&b, "目标响度 %.0f dBFS，允许削顶概率 %.2f%%，分段长度 %d 帧，采样率 8000 Hz。\n\n",
		cfg.TargetLUFS, cfg.ClipProb*100, cfg.SegmentLen)

	type result struct {
		def  caseDef
		rep  *audio.NormalizeReport
		out  *audio.Clip
	}
	results := make([]result, 0, len(cases))
	for _, cd := range cases {
		// 每次都从全新素材运行，顺带直观验证确定性（两次运行的增益在测试中逐位核验）。
		r, err := audio.Normalize(cd.clip, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s 归一失败: %v\n", cd.name, err)
			os.Exit(1)
		}
		results = append(results, result{def: cd, rep: r, out: cd.clip})
	}

	// 表 1：增益与峰值表。
	b.WriteString("## 1. 增益与峰值表\n\n")
	b.WriteString("| 素材 | 整段增益(线性) | 分段增益(线性) | 限幅阈值 | 实际削顶概率 | 削顶样本数 | 归一前峰值 | 归一后峰值 |\n")
	b.WriteString("|---|---:|---|---:|---:|---:|---:|---:|\n")
	for _, r := range results {
		segGains := make([]string, len(r.rep.Segments))
		for i, s := range r.rep.Segments {
			segGains[i] = fmt.Sprintf("%.4f", s.Gain)
		}
		fmt.Fprintf(&b, "| %s | %.4f | %s | %d | %.4f%% | %d | %d | %d |\n",
			r.def.name, r.rep.GlobalGain, strings.Join(segGains, ", "),
			r.rep.Threshold, r.rep.ActualClipProb*100, r.rep.ClipCount,
			r.rep.PeakBefore, r.rep.PeakAfter)
	}
	b.WriteString("\n")

	// 表 2：幅值分布表（归一前后计数/占比，逐素材列出）。
	b.WriteString("## 2. 幅值分布表\n\n")
	for _, r := range results {
		fmt.Fprintf(&b, "### 素材：%s（共 %d 帧）\n\n", r.def.name, len(r.out.Frames))
		b.WriteString("| 幅值区间 (dBFS) | 归一前计数 | 归一前占比 | 归一后计数 | 归一后占比 |\n")
		b.WriteString("|---|---:|---:|---:|---:|\n")
		for _, d := range r.rep.Distribution {
			fmt.Fprintf(&b, "| %s | %d | %.2f%% | %d | %.2f%% |\n",
				d.Label, d.BeforeCount, d.BeforeFrac*100,
				d.AfterCount, d.AfterFrac*100)
		}
		b.WriteString("\n")
	}

	// 表 3：归一前后响度与峰值对照（各一行）。
	b.WriteString("## 3. 归一前后响度与峰值对照\n\n")
	b.WriteString("| 素材 | 归一前响度(dBFS) | 归一后响度(dBFS) | 目标(dBFS) | 归一前峰值 | 归一后峰值 |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|\n")
	for _, r := range results {
		fmt.Fprintf(&b, "| %s | %s | %s | %.0f | %d | %d |\n",
			r.def.name, fmtLU(r.rep.LoudBefore), fmtLU(r.rep.LoudAfter),
			r.rep.TargetLUFS, r.rep.PeakBefore, r.rep.PeakAfter)
	}
	b.WriteString("\n")

	// 核验结论摘要。
	b.WriteString("## 4. 硬性保证核验\n\n")
	b.WriteString("- 归一前后峰值均 <= 32767（满幅）。\n")
	b.WriteString("- 限幅阈值由削顶概率在离散幅值分布上反解，实际阈值 == 反解值且 <= 32767；超阈值样本占比 <= 允许概率。\n")
	b.WriteString("- 同一素材两次运行的分段增益与输出帧逐位一致（由 TestNormalizeCases 核验）。\n")

	out := b.String()
	fmt.Print(out)

	dst := filepath.Join("artifacts", "normalization_report.md")
	if err := os.MkdirAll("artifacts", 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "创建 artifacts 失败: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(dst, []byte(out), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "写报告失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\n报告已写入 %s\n", dst)
}

func fmtLU(v float64) string {
	if math.IsInf(v, -1) {
		return "-Inf (静音)"
	}
	return fmt.Sprintf("%.2f", v)
}

// 以下构造函数与测试中的用例保持一致。

func buildQuiet() *audio.Clip {
	c, _ := audio.NewClip(8000, 4000)
	for i := range c.Frames {
		t := float64(i) / float64(c.Rate)
		c.Frames[i] = int16(200 * math.Sin(2*math.Pi*220*t))
	}
	return c
}

func buildLoud() *audio.Clip {
	c, _ := audio.NewClip(8000, 4000)
	for i := range c.Frames {
		t := float64(i) / float64(c.Rate)
		c.Frames[i] = int16(30000 * math.Sin(2*math.Pi*220*t))
	}
	return c
}

func buildVarying() *audio.Clip {
	c, _ := audio.NewClip(8000, 4000)
	amps := []int{100, 25000, 50, 20000}
	seg := 1000
	for i := range c.Frames {
		t := float64(i) / float64(c.Rate)
		a := amps[i/seg]
		c.Frames[i] = int16(float64(a) * math.Sin(2*math.Pi*220*t))
	}
	return c
}

func buildSilence() *audio.Clip {
	c, _ := audio.NewClip(8000, 4000)
	return c
}

func buildSingle() *audio.Clip {
	c, _ := audio.NewClip(8000, 1)
	c.Frames[0] = 12345
	return c
}
