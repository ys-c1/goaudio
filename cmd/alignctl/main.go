// alignctl 是多声道延迟对齐的命令行入口。
//
// 场景模式（五个固定场景，输出延迟表与峰值提升）：
//
//	go run ./cmd/alignctl -case inphase|inverse|shift1|shift10|silent [-w 8]
//
// 非法输入复现模式（三类，各一条可跑命令）：
//
//	go run ./cmd/alignctl -illegal fewchannels
//	go run ./cmd/alignctl -illegal outofwindow
//	go run ./cmd/alignctl -illegal ratemismatch
package main

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"os"

	"goaudio/audio"
)

const frames = 4096

func main() {
	caseName := flag.String("case", "", "对齐场景: inphase|inverse|shift1|shift10|silent")
	illegal := flag.String("illegal", "", "非法输入复现: fewchannels|outofwindow|ratemismatch")
	w := flag.Int("w", 8, "搜索窗口半宽（最大延迟估计值）")
	flag.Parse()

	switch {
	case *illegal != "":
		if err := runIllegal(*illegal, *w); err != nil {
			fmt.Fprintf(os.Stderr, "退出（非法输入）: %v\n", err)
			os.Exit(1)
		}
	case *caseName != "":
		if err := runCase(*caseName, *w); err != nil {
			fmt.Fprintf(os.Stderr, "对齐失败: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "用法: go run ./cmd/alignctl -case inphase|inverse|shift1|shift10|silent")
		fmt.Fprintln(os.Stderr, "      go run ./cmd/alignctl -illegal fewchannels|outofwindow|ratemismatch")
		os.Exit(2)
	}
}

// drumRef 与测试中的鼓点参考信号同构（独立实现，避免依赖测试内部函数）。
func drumRef(n int) []int16 {
	out := make([]int16, n)
	seed := uint32(0x1234abcd)
	for i := 0; i < n; i++ {
		seed = seed*1103515245 + 12345
		r := float64(int32(seed)) / float64(1<<31)
		env := math.Exp(-float64(i) / float64(n/4))
		v := r * env * 20000
		if v > 32767 {
			v = 32767
		}
		if v < -32768 {
			v = -32768
		}
		out[i] = int16(v)
	}
	return out
}

func shift(sig []int16, d int, invert bool) []int16 {
	out := make([]int16, len(sig))
	for i := range sig {
		if i-d >= 0 {
			v := sig[i-d]
			if invert {
				v = -v
			}
			out[i] = v
		}
	}
	return out
}

func runCase(name string, w int) error {
	ref := drumRef(frames)
	var chans [][]int16
	switch name {
	case "inphase":
		chans = [][]int16{ref, shift(ref, 0, false)}
	case "inverse":
		chans = [][]int16{ref, shift(ref, 0, true)}
	case "shift1":
		chans = [][]int16{ref, shift(ref, 1, false)}
	case "shift10":
		if w < 10 {
			fmt.Fprintf(os.Stderr, "提示: 场景 shift10 真实延迟为 10，当前 w=%d 将触发超窗报错；如需对齐请用 -w 16\n", w)
		}
		chans = [][]int16{ref, shift(ref, 10, false)}
	case "silent":
		chans = [][]int16{ref, make([]int16, frames)}
	default:
		return fmt.Errorf("未知场景 %q", name)
	}
	mc := &audio.MultiClip{Rate: 8000, Channels: chans}
	rep, err := audio.AlignChannels(mc, w)
	if err != nil {
		return err
	}
	printReport(name, w, rep)
	return nil
}

func printReport(name string, w int, rep *audio.AlignReport) {
	fmt.Printf("场景=%s  声道数=%d  总帧数=%d（对齐前后不变）  窗口估计半宽 W=%d\n",
		name, rep.Channels, rep.Frames, w)
	fmt.Println("延迟表（相对主声道 0；lag>0 表示该路滞后）:")
	fmt.Println("通道  延迟(样本)  极性    静音  对齐前|峰值|  对齐后|峰值|  提升      轮数  窗口说明")
	totalRounds := 0
	for _, r := range rep.Results {
		pol := "+1"
		if r.Polarity < 0 {
			pol = "-1"
		}
		if r.Silent {
			pol = " 0"
		}
		gain := math.Abs(r.PeakAfter) - math.Abs(r.PeakBefore)
		fmt.Printf("%-4d  %-9d  %-4s  %-5t  %.6f      %.6f      %+.6f  %-4d  %s\n",
			r.Channel, r.Delay, pol, r.Silent,
			math.Abs(r.PeakBefore), math.Abs(r.PeakAfter), gain,
			r.Rounds, r.WindowNote)
		totalRounds += r.Rounds
	}
	fmt.Printf("轮数统计: 总搜索轮数=%d（每路每候选 lag 计一轮；静音路 0 轮）\n", totalRounds)
}

func runIllegal(kind string, w int) error {
	switch kind {
	case "fewchannels":
		// 仅 1 路
		mc := &audio.MultiClip{Rate: 8000, Channels: [][]int16{make([]int16, 64)}}
		_, err := audio.AlignChannels(mc, w)
		reportIllegal("声道数不足二", err, audio.ErrTooFewChannels)
		return err
	case "outofwindow":
		// 真实延迟 10，窗口仅 [-8,+8]
		ref := drumRef(frames)
		mc := &audio.MultiClip{Rate: 8000, Channels: [][]int16{ref, shift(ref, 10, false)}}
		_, err := audio.AlignChannels(mc, 8)
		reportIllegal("延迟超出窗口", err, audio.ErrDelayOutOfWindow)
		return err
	case "ratemismatch":
		// 通道 1 采样率与主声道不同
		c1 := &audio.Clip{Rate: 8000, Frames: make([]int16, 64)}
		c2 := &audio.Clip{Rate: 16000, Frames: make([]int16, 64)}
		_, err := audio.MultiClipFromClips(c1, c2)
		reportIllegal("采样率不一致", err, audio.ErrRateMismatch)
		return err
	default:
		return fmt.Errorf("未知非法输入类型 %q", kind)
	}
}

func reportIllegal(label string, err error, sentinel error) {
	fmt.Printf("复现类型: %s\n", label)
	if errors.Is(err, sentinel) {
		fmt.Printf("已按预期报出哨兵错误 %q，完整信息: %v\n", sentinel, err)
	} else {
		fmt.Printf("警告: 错误类型不符: %v\n", err)
	}
}
