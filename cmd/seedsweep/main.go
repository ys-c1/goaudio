// 命令 seedsweep 用多种子扫描抖动量化的两条不变量，确认其不依赖单一种子。
package main

import (
	"fmt"
	"os"

	"goaudio/audio"
)

func main() {
	const bitDepth = 8
	delta, err := audio.QuantStep(bitDepth)
	if err != nil {
		fatal(err)
	}
	// 不同输入形态：极轻正弦、全零、直流、随机小值、正常正弦
	build := func(kind string) []int16 {
		switch kind {
		case "极轻正弦":
			c, _ := audio.NewClip(8000, 2048)
			_ = audio.Sine(c, 100, 2)
			return c.Frames
		case "全零":
			return make([]int16, 2048)
		case "直流1":
			f := make([]int16, 2048)
			for i := range f {
				f[i] = 1
			}
			return f
		case "直流127": // 恰在死区边界 Δ/2=128 内侧
			f := make([]int16, 2048)
			for i := range f {
				f[i] = 127
			}
			return f
		case "正常正弦":
			c, _ := audio.NewClip(8000, 2048)
			_ = audio.Sine(c, 100, 8000)
			return c.Frames
		}
		return nil
	}

	kinds := []string{"极轻正弦", "全零", "直流1", "直流127", "正常正弦"}
	var failInv1, failInv2, total int
	for seed := uint64(1); seed <= 200; seed++ {
		for _, kind := range kinds {
			frames := build(kind)
			c := &audio.Clip{Rate: 8000, Frames: frames}
			out1, err := audio.DitherQuantize(c, bitDepth, seed, 0)
			if err != nil {
				fatal(err)
			}
			out2, err := audio.DitherQuantize(c, bitDepth, seed, 0)
			if err != nil {
				fatal(err)
			}
			plain, err := audio.Quantize(c, bitDepth)
			if err != nil {
				fatal(err)
			}
			total++

			// 不变量 2：逐样本复现
			for i := range out1 {
				if out1[i] != out2[i] {
					failInv2++
					fmt.Printf("种子 %d %s：第 %d 帧不可复现\n", seed, kind, i)
					break
				}
			}
			// 不变量 1：死区静音比例不上升（对输入 + 对无抖动基线）
			inD := audio.DeadbandSilenceRatio(frames, delta)
			plD := audio.DeadbandSilenceRatio(plain, delta)
			outD := audio.DeadbandSilenceRatio(out1, delta)
			if outD > inD || outD > plD {
				failInv1++
				fmt.Printf("种子 %d %s：死区静音比例上升 in=%.4f plain=%.4f out=%.4f\n",
					seed, kind, inD, plD, outD)
			}
		}
	}
	fmt.Printf("扫描完成：共 %d 组合（200 种子 × 5 形态），不变量1失败 %d，不变量2失败 %d\n",
		total, failInv1, failInv2)
	if failInv1 > 0 || failInv2 > 0 {
		os.Exit(1)
	}
	fmt.Println("全部通过：两条不变量在 200 个种子下均成立。")
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
