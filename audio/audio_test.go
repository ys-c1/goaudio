package audio

import (
	"path/filepath"
	"testing"
)

func TestSineAmplitude(t *testing.T) {
	c, err := NewClip(8000, 800)
	if err != nil {
		t.Fatal(err)
	}
	if err := Sine(c, 100, 10000); err != nil {
		t.Fatal(err)
	}
	if p := Peak(c); p < 9000 || p > 10000 {
		t.Fatalf("峰值异常 %d", p)
	}
	if err := Sine(c, 5000, 100); err == nil {
		t.Fatal("超过奈奎斯特应报错")
	}
}

func TestEnvelopeEndpoints(t *testing.T) {
	c, _ := NewClip(8000, 100)
	for i := range c.Frames {
		c.Frames[i] = 1000
	}
	if err := ApplyEnvelope(c, 10, 10); err != nil {
		t.Fatal(err)
	}
	if c.Frames[0] != 0 || c.Frames[99] != 0 {
		t.Fatalf("端点应为零 %d %d", c.Frames[0], c.Frames[99])
	}
	if err := ApplyEnvelope(c, 60, 60); err == nil {
		t.Fatal("进出场超出长度应报错")
	}
}

func TestWAVRoundTrip(t *testing.T) {
	c, _ := NewClip(44100, 128)
	if err := Sine(c, 440, 8000); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "a.wav")
	if err := c.SaveWAV(p); err != nil {
		t.Fatal(err)
	}
	got, err := LoadWAV(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rate != 44100 || len(got.Frames) != 128 {
		t.Fatalf("尺寸错误 %d %d", got.Rate, len(got.Frames))
	}
	for i := range got.Frames {
		if got.Frames[i] != c.Frames[i] {
			t.Fatalf("第 %d 帧不一致", i)
		}
	}
}

func TestNewClipErrors(t *testing.T) {
	if _, err := NewClip(0, 10); err == nil {
		t.Fatal("采样率为零应报错")
	}
	if _, err := NewClip(8000, 0); err == nil {
		t.Fatal("帧数为零应报错")
	}
}
