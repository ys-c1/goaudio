// 命令 ditherdemo 对五类用例（极轻、正常、满幅、静音、单样本）执行
// 固定种子 TPDF 抖动量化，逐条核验静音比例与可复现两条不变量，
// 输出信噪比与幅度表、误差分布、抖动序列前 16 项，并把报告写入
// artifacts/dither_report.txt。
package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"goaudio/audio"
)

const (
	bitDepth = 8
	seed     = uint64(20261124)
	nLong    = 4096
	rate     = 8000
)

type report struct{ b strings.Builder }

func (r *report) line(format string, args ...any) {
	fmt.Fprintf(&r.b, format+"\n", args...)
}

func (r *report) blank() { r.b.WriteByte('\n') }

type caseDef struct {
	name   string
	frames []int16
}

func buildCases() ([]caseDef, error) {
	mk := func(amp int) (*audio.Clip, error) {
		c, err := audio.NewClip(rate, nLong)
		if err != nil {
			return nil, err
		}
		if amp > 0 {
			if err := audio.Sine(c, 100, amp); err != nil {
				return nil, err
			}
		}
		return c, nil
	}
	light, err := mk(2) // 极轻：8-bit Δ=256，无抖动整段塌静音
	if err != nil {
		return nil, err
	}
	normal, err := mk(8000)
	if err != nil {
		return nil, err
	}
	full, err := mk(32767)
	if err != nil {
		return nil, err
	}
	silent, err := mk(0)
	if err != nil {
		return nil, err
	}
	one, err := audio.NewClip(rate, 1)
	if err != nil {
		return nil, err
	}
	one.Frames[0] = 1
	return []caseDef{
		{"极轻(amp=2)", light.Frames},
		{"正常(amp=8000)", normal.Frames},
		{"满幅(amp=32767)", full.Frames},
		{"静音(全零)", silent.Frames},
		{"单样本(x=1)", one.Frames},
	}, nil
}

func main() {
	r := &report{}
	delta, err := audio.QuantStep(bitDepth)
	if err != nil {
		fatal(err)
	}
	nativeSNR, err := audio.NativeTPDFSNR(bitDepth)
	if err != nil {
		fatal(err)
	}
	targetRMS, err := audio.TargetDitherRMS(bitDepth, nativeSNR)
	if err != nil {
		fatal(err)
	}

	r.line("================ 抖动量化核验报告 ================")
	r.line("位深 B=%d  量化步长 Δ=%.0f  种子 seed=%d  目标SNR=%.3f dB（该位深固有 TPDF SNR）",
		bitDepth, delta, seed, nativeSNR)
	r.line("满幅参考 FullScale=%.0f，抖动类型 TPDF（双均匀和，[-1,1) 归一后标定）", audio.FullScale)
	r.blank()

	// 抖动序列前 16 项（标定后的实际施加值与归一化原始值各打印一份）
	raw16, err := audio.DitherSequence(16, seed)
	if err != nil {
		fatal(err)
	}
	cal16, _, err := audio.CalibratedDither(16, bitDepth, seed, nativeSNR)
	if err != nil {
		fatal(err)
	}
	r.line("---- 抖动序列前 16 项（种子 %d）----", seed)
	r.line("%-4s %18s %18s", "i", "归一化 TPDF [-1,1)", "实际施加值(样本域)")
	for i := range raw16 {
		r.line("%-4d %+18.10f %+18.6f", i, raw16[i], cal16[i])
	}
	r.blank()

	cases, err := buildCases()
	if err != nil {
		fatal(err)
	}

	r.line("---- 用例核验（两条不变量逐条）----")
	r.line("不变量1：死区静音比例（|x|<Δ/2，与量化器判零同口径）量化后不得上升")
	r.line("不变量2：同一种子两次运行逐样本相同")
	r.blank()
	header := "%-16s %8s %8s %8s | %8s %8s | %8s %8s | %s"
	r.line(header, "用例", "输入死区", "无抖动", "抖动后", "输入零", "输出零", "输出RMS", "实测SNR", "不变量")
	allPass := true
	var lightDist audio.ErrorDist
	for _, cd := range cases {
		c := &audio.Clip{Rate: rate, Frames: cd.frames}
		out1, err := audio.DitherQuantize(c, bitDepth, seed, nativeSNR)
		if err != nil {
			fatal(err)
		}
		out2, err := audio.DitherQuantize(c, bitDepth, seed, nativeSNR)
		if err != nil {
			fatal(err)
		}
		plain, err := audio.Quantize(c, bitDepth)
		if err != nil {
			fatal(err)
		}

		// 不变量 2 逐样本核验
		rep2 := true
		for i := range out1 {
			if out1[i] != out2[i] {
				rep2 = false
				allPass = false
			}
		}
		// 不变量 1：对输入、对无抖动基线两个口径
		inDead := audio.DeadbandSilenceRatio(c.Frames, delta)
		plainDead := audio.DeadbandSilenceRatio(plain, delta)
		outDead := audio.DeadbandSilenceRatio(out1, delta)
		inv1a, inv1b := outDead <= inDead, outDead <= plainDead
		if !(inv1a && inv1b && rep2) {
			allPass = false
		}
		verdict := fmt.Sprintf("1a=%v 1b=%v 2=%v", inv1a, inv1b, rep2)

		r.line("%-16s %8.4f %8.4f %8.4f | %8.4f %8.4f | %8.2f %7.2fdB | %s",
			cd.name, inDead, plainDead, outDead,
			audio.SilenceRatio(c.Frames), audio.SilenceRatio(out1),
			audio.RMS(out1), audio.MeasuredSNR(out1), verdict)

		if strings.HasPrefix(cd.name, "极轻") {
			lightDist = audio.NewErrorDist(c.Frames, out1, delta)
		}
	}
	r.blank()
	if allPass {
		r.line(">>> 两条不变量在全部 5 个用例上逐条通过。")
	} else {
		r.line(">>> 存在不变量失败！")
	}
	r.blank()

	// 幅度核验：实测抖动 RMS vs SNR 推算值
	r.line("---- 抖动幅度核验（实测高于推算即未完成）----")
	dither, _, err := audio.CalibratedDither(nLong, bitDepth, seed, nativeSNR)
	if err != nil {
		fatal(err)
	}
	measuredRMS := rms(dither)
	r.line("推算幅度 σ = FullScale/sqrt(2·10^(SNR/10)) = %.6f（亦等于 Δ/√6 = %.6f）",
		targetRMS, delta/math.Sqrt(6))
	r.line("实测幅度（施加序列 RMS，n=%d）= %.6f，比值 %.6f（安全余量 0.999）",
		nLong, measuredRMS, measuredRMS/targetRMS)
	if measuredRMS > targetRMS {
		r.line(">>> 实测幅度高于推算值，按未完成计。")
		allPass = false
	} else {
		r.line(">>> 实测幅度不高于推算值，核验通过。")
	}
	r.blank()

	// 位深扫描：信噪比与幅度表
	r.line("---- 信噪比与幅度表（位深扫描，同种子 %d，n=%d）----", seed, nLong)
	r.line("%-6s %8s %12s %14s %14s %10s", "位深B", "Δ", "推算SNR(dB)", "推算抖动RMS", "实测抖动RMS", "实测/推算")
	for _, b := range []int{4, 8, 12, 16} {
		d, err := audio.QuantStep(b)
		if err != nil {
			fatal(err)
		}
		snr, err := audio.NativeTPDFSNR(b)
		if err != nil {
			fatal(err)
		}
		want, err := audio.TargetDitherRMS(b, snr)
		if err != nil {
			fatal(err)
		}
		seq, _, err := audio.CalibratedDither(nLong, b, seed, snr)
		if err != nil {
			fatal(err)
		}
		got := rms(seq)
		r.line("%-6d %8.0f %12.3f %14.6f %14.6f %10.6f", b, d, snr, want, got, got/want)
		if got > want {
			allPass = false
		}
	}
	r.blank()

	// 误差分布（极轻用例，最能体现抖动效果）
	r.line("---- 误差分布（极轻用例 amp=2，误差=输出-输入，箱宽 Δ/2）----")
	r.line(lightDist.String())
	r.blank()

	// 参数错误各报参数名
	r.line("---- 参数错误（各报出参数名）----")
	c := &audio.Clip{Rate: rate, Frames: make([]int16, 8)}
	r.line("bitDepth=0  -> %v", expectErr(audio.DitherQuantize(c, 0, seed, 0)))
	r.line("bitDepth=17 -> %v", expectErr(audio.DitherQuantize(c, 17, seed, 0)))
	r.line("seed=0      -> %v", expectErr(audio.DitherQuantize(c, 8, 0, 0)))
	r.line("rate=0      -> %v", expectErr(audio.DitherQuantize(&audio.Clip{Frames: c.Frames}, 8, seed, 0)))
	r.blank()
	r.line("总体结论：%v", allPass)

	out := r.b.String()
	fmt.Print(out)
	path := filepath.Join("artifacts", "dither_report.txt")
	if err := os.MkdirAll("artifacts", 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("\n报告已写入 %s\n", path)
}

func expectErr(_ []int16, err error) error { return err }

func rms(x []float64) float64 {
	var s float64
	for _, v := range x {
		s += v * v
	}
	return math.Sqrt(s / float64(len(x)))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
