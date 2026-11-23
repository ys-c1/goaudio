package audio

import (
	"encoding/json"
	"os"
)

// TableEntry 是“时间常数与过冲量表”的一行：一种素材的实测时间常数、
// 反解起停时间与实测过冲/纹波。
type TableEntry struct {
	Material          string  `json:"material"`
	SampleRate        int     `json:"sampleRate"`
	DurationFrames    int     `json:"durationFrames"`
	Threshold         int     `json:"threshold"`
	AttackTauSamples  float64 `json:"attackTauSamples"`
	AttackTauSeconds  float64 `json:"attackTauSeconds"`
	AttackFitR2       float64 `json:"attackFitR2"`
	ReleaseTauSamples float64 `json:"releaseTauSamples"`
	ReleaseTauSeconds float64 `json:"releaseTauSeconds"`
	ReleaseFitR2      float64 `json:"releaseFitR2"`
	AttackSamples     int     `json:"attackSamples"`
	AttackSeconds     float64 `json:"attackSeconds"`
	ReleaseSamples    int     `json:"releaseSamples"`
	ReleaseSeconds    float64 `json:"releaseSeconds"`
	AllowedOvershoot  float64 `json:"allowedOvershoot"`
	MeasuredOvershoot float64 `json:"measuredOvershoot"`
	AllowedRipple     float64 `json:"allowedRipple"`
	MeasuredRipple    float64 `json:"measuredRipple"`
	Limited           bool    `json:"limited"`
}

// Registry 收尾登记表：收集每段素材的时间常数与过冲量条目。
type Registry struct {
	Entries []TableEntry `json:"entries"`
}

// NewRegistry 创建空登记表。
func NewRegistry() *Registry { return &Registry{} }

// Register 把一份反解报告登记进表。
func (r *Registry) Register(material MaterialKind, rep *AdaptiveReport) {
	r.Entries = append(r.Entries, TableEntry{
		Material:          string(material),
		SampleRate:        rep.SampleRate,
		DurationFrames:    rep.DurationFrames,
		Threshold:         rep.Threshold,
		AttackTauSamples:  rep.Tau.AttackTauSamples,
		AttackTauSeconds:  rep.Tau.AttackTauSeconds,
		AttackFitR2:       rep.Tau.AttackR2,
		ReleaseTauSamples: rep.Tau.ReleaseTauSamples,
		ReleaseTauSeconds: rep.Tau.ReleaseTauSeconds,
		ReleaseFitR2:      rep.Tau.ReleaseR2,
		AttackSamples:     rep.AttackSamples,
		AttackSeconds:     rep.AttackSeconds,
		ReleaseSamples:    rep.ReleaseSamples,
		ReleaseSeconds:    rep.ReleaseSeconds,
		AllowedOvershoot:  rep.AllowedOvershoot,
		MeasuredOvershoot: rep.MeasuredOvershoot,
		AllowedRipple:     rep.AllowedRipple,
		MeasuredRipple:    rep.MeasuredRipple,
		Limited:           rep.Limited,
	})
}

// WriteJSON 把登记表写到 JSON 文件，作为收尾产物。
func (r *Registry) WriteJSON(path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// LoadRegistry 从 JSON 文件读回登记表（测试与复核用）。
func LoadRegistry(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Registry
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
