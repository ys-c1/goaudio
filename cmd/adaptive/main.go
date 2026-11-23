// Command adaptive 演示起停时间反解流程：
// 从鼓点、人声、弦乐、静音、极短片段各取一段，实测一阶响应并拟合时间常数，
// 按允许过冲反解起音时间、按允许纹波反解释放时间，两条分别验证，
// 最后把“时间常数与过冲量表”登记并写到 JSON。
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"goaudio/audio"
)

func main() {
	const (
		rate             = 48000
		frames           = 24000 // 0.5s
		threshold        = 10000
		allowedOvershoot = 800  // 电平单位：起音后峰值最多超出阈值 800
		allowedRipple    = 1500 // 电平单位：释放纹波最大偏差 1500
	)

	kinds := []audio.MaterialKind{
		audio.MaterialDrum,
		audio.MaterialVocal,
		audio.MaterialStrings,
		audio.MaterialSilence,
		audio.MaterialTiny,
	}
	durations := map[audio.MaterialKind]int{
		audio.MaterialDrum:    frames,
		audio.MaterialVocal:   frames,
		audio.MaterialStrings: frames,
		audio.MaterialSilence: frames,
		audio.MaterialTiny:    8, // 极短片段
	}

	registry := audio.NewRegistry()

	fmt.Println("素材\t时长(帧)\tτ起音(样本)\tR²起音\tτ释放(样本)\tR²释放\t起音(帧)\t释放(帧)\t实测过冲\t实测纹波")
	for _, kind := range kinds {
		dur := durations[kind]
		clip, err := audio.MakeMaterial(kind, rate, dur, threshold)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s 素材生成失败: %v\n", kind, err)
			os.Exit(1)
		}
		levels := audio.AbsLevels(clip)
		rep, err := audio.SolveAdaptiveTimes(rate, dur, threshold, levels, allowedOvershoot, allowedRipple)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s 反解失败: %v\n", kind, err)
			os.Exit(1)
		}
		registry.Register(kind, rep)

		// 两条约束分开再验一次，命令行层面点名结果。
		if rep.Limited {
			if _, err := audio.VerifyAttackOvershoot(threshold, rep.AttackSamples, rep.ReleaseSamples, levels, allowedOvershoot); err != nil {
				fmt.Fprintf(os.Stderr, "%s 起音过冲验证失败: %v\n", kind, err)
				os.Exit(1)
			}
			if _, err := audio.VerifyReleaseRipple(threshold, rep.AttackSamples, rep.ReleaseSamples, levels, allowedRipple); err != nil {
				fmt.Fprintf(os.Stderr, "%s 释放纹波验证失败: %v\n", kind, err)
				os.Exit(1)
			}
		}
		fmt.Printf("%s\t%d\t%.2f\t%.4f\t%.2f\t%.4f\t%d\t%d\t%.2f\t%.2f\n",
			kind, dur,
			rep.Tau.AttackTauSamples, rep.Tau.AttackR2,
			rep.Tau.ReleaseTauSamples, rep.Tau.ReleaseR2,
			rep.AttackSamples, rep.ReleaseSamples,
			rep.MeasuredOvershoot, rep.MeasuredRipple)
	}

	out := filepath.Join("artifacts", "time_constant_overshoot_table.json")
	if err := registry.WriteJSON(out); err != nil {
		fmt.Fprintf(os.Stderr, "登记表写入失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\n时间常数与过冲量表已登记并写入 %s（共 %d 条）\n", out, len(registry.Entries))
}
