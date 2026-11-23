package audio

import (
	"fmt"
	"math"
)

// ResampleParams 描述窗函数 sinc 重采样核的参数。
type ResampleParams struct {
	KernelLen  int     // 核长（以输入样本计），必须为正奇数
	Oversample int     // 核过采样率（每输入样本的多相表相位数），必须为正
	Beta       float64 // Kaiser 窗 β；0 表示矩形窗
}

// SolveResample 由目标幅度谱误差上界 eps（相对值，0<eps<1）
// 反推窗函数重采样所需的核长与过采样率。
//
// 反推过程：
//  1. 所需阻带衰减 A = -20·log10(eps) + 15dB 工程裕量；
//  2. Kaiser 窗 β 由 A 的经验公式给出；
//  3. 过渡带取截止带的 1/10，即 Δω = 0.1π·min(1, dst/src)，
//     由 Kaiser 阶数公式 N ≥ (A-8)/(2.285·Δω) 解出核长并向上取奇数；
//  4. 多相表相邻相位线性插值误差 ≤ |h''|/(8P²) ≤ π²/(8P²)，
//     令其不超过 eps/10，解出过采样率 P ≥ π/√(0.8·eps)。
func SolveResample(srcRate, dstRate int, eps float64) (ResampleParams, error) {
	if srcRate <= 0 {
		return ResampleParams{}, fmt.Errorf("%w: 源采样率 %d", ErrSampleRate, srcRate)
	}
	if dstRate <= 0 {
		return ResampleParams{}, fmt.Errorf("%w: 目标采样率 %d", ErrSampleRate, dstRate)
	}
	if eps <= 0 || eps >= 1 {
		return ResampleParams{}, fmt.Errorf("%w: %v", ErrTarget, eps)
	}
	g := float64(dstRate) / float64(srcRate)
	a := -20*math.Log10(eps) + 15 // 设计衰减，含 15dB 裕量
	beta := kaiserBeta(a)
	dw := 0.1 * math.Pi * math.Min(1, g)
	n := (a - 8) / (2.285 * dw)
	if n < 3 {
		n = 3
	}
	l := int(math.Ceil(n))
	if l%2 == 0 {
		l++
	}
	p := int(math.Ceil(math.Pi / math.Sqrt(0.8*eps)))
	if p < 1 {
		p = 1
	}
	return ResampleParams{KernelLen: l, Oversample: p, Beta: beta}, nil
}

// Resample 用窗函数 sinc 核把 c 重采样到 targetRate。
// targetRate == c.Rate 时走原采样率路径，逐样本原样返回副本。
func Resample(c *Clip, targetRate int, p ResampleParams) (*Clip, error) {
	if targetRate <= 0 {
		return nil, fmt.Errorf("%w: %d", ErrSampleRate, targetRate)
	}
	if p.KernelLen <= 0 || p.KernelLen%2 == 0 {
		return nil, fmt.Errorf("%w: %d", ErrKernelLen, p.KernelLen)
	}
	if p.Oversample <= 0 {
		return nil, fmt.Errorf("%w: %d", ErrOversample, p.Oversample)
	}
	out := &Clip{Rate: targetRate}
	if c.Rate == targetRate {
		out.Frames = append([]int16(nil), c.Frames...)
		return out, nil
	}
	g := float64(targetRate) / float64(c.Rate)
	fc := math.Min(1, g) / 2 // 截止频率（按源采样率归一），升降采样都抗镜像/抗混叠
	tab := buildKernel(p.KernelLen, p.Oversample, fc, p.Beta)
	n := len(c.Frames)
	nOut := int(math.Round(float64(n) * g))
	if nOut < 1 {
		nOut = 1
	}
	out.Frames = make([]int16, nOut)
	half := (p.KernelLen - 1) / 2
	ov := float64(p.Oversample)
	for j := 0; j < nOut; j++ {
		t := float64(j) / g // 输出时刻，以输入样本为单位
		lo := int(math.Ceil(t)) - half
		hi := int(math.Floor(t)) + half
		acc := 0.0
		for i := lo; i <= hi; i++ {
			if i < 0 || i >= n {
				continue
			}
			d := t - float64(i)
			if d < 0 {
				d = -d
			}
			x := d * ov
			k := int(x)
			var h float64
			if k >= len(tab)-1 {
				h = tab[len(tab)-1]
			} else {
				frac := x - float64(k)
				h = tab[k] + (tab[k+1]-tab[k])*frac
			}
			acc += float64(c.Frames[i]) * h
		}
		out.Frames[j] = toInt16(acc)
	}
	return out, nil
}

// buildKernel 生成半核多相表：tab[k] 对应距离 d = k/Oversample（输入样本），
// h(d) = 2fc·sinc(2fc·d)·w(d)，w 为 Kaiser 窗，表长 half*Oversample+1。
func buildKernel(kernelLen, oversample int, fc, beta float64) []float64 {
	half := (kernelLen - 1) / 2
	tab := make([]float64, half*oversample+1)
	den := besselI0(beta)
	for k := range tab {
		d := float64(k) / float64(oversample)
		w := 1.0
		if half > 0 {
			x := d / float64(half)
			w = besselI0(beta*math.Sqrt(math.Max(0, 1-x*x))) / den
		}
		tab[k] = 2 * fc * sinc(2*fc*d) * w
	}
	return tab
}

func kaiserBeta(a float64) float64 {
	switch {
	case a > 50:
		return 0.1102 * (a - 8.7)
	case a >= 21:
		d := a - 21
		return 0.5842*math.Pow(d, 0.4) + 0.07886*d
	default:
		return 0
	}
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	x *= math.Pi
	return math.Sin(x) / x
}

// besselI0 零阶第一类修正贝塞尔函数（级数求和）。
func besselI0(x float64) float64 {
	sum, term := 1.0, 1.0
	y := x * x / 4
	for k := 1; k < 200; k++ {
		term *= y / float64(k*k)
		sum += term
		if term < 1e-15*sum {
			break
		}
	}
	return sum
}

func toInt16(v float64) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(math.Round(v))
}
