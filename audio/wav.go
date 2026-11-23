package audio

import (
	"encoding/binary"
	"os"
)

type Clip struct {
	Rate   int
	Frames []int16
}

func NewClip(rate, frames int) (*Clip, error) {
	if rate <= 0 {
		return nil, ErrSampleRate
	}
	if frames <= 0 {
		return nil, ErrFrames
	}
	return &Clip{Rate: rate, Frames: make([]int16, frames)}, nil
}

func (c *Clip) SaveWAV(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dataLen := 2 * len(c.Frames)
	buf := make([]byte, 44+dataLen)
	copy(buf[0:], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:], uint32(36+dataLen))
	copy(buf[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(buf[16:], 16)
	binary.LittleEndian.PutUint16(buf[20:], 1)
	binary.LittleEndian.PutUint16(buf[22:], 1)
	binary.LittleEndian.PutUint32(buf[24:], uint32(c.Rate))
	binary.LittleEndian.PutUint32(buf[28:], uint32(c.Rate*2))
	binary.LittleEndian.PutUint16(buf[32:], 2)
	binary.LittleEndian.PutUint16(buf[34:], 16)
	copy(buf[36:], "data")
	binary.LittleEndian.PutUint32(buf[40:], uint32(dataLen))
	for i, v := range c.Frames {
		binary.LittleEndian.PutUint16(buf[44+2*i:], uint16(v))
	}
	_, err = f.Write(buf)
	return err
}

func LoadWAV(path string) (*Clip, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 44 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, ErrFormat
	}
	rate := int(binary.LittleEndian.Uint32(data[24:]))
	dataLen := int(binary.LittleEndian.Uint32(data[40:]))
	if rate <= 0 || dataLen < 0 || 44+dataLen > len(data) || dataLen%2 != 0 {
		return nil, ErrFormat
	}
	c := &Clip{Rate: rate, Frames: make([]int16, dataLen/2)}
	for i := range c.Frames {
		c.Frames[i] = int16(binary.LittleEndian.Uint16(data[44+2*i:]))
	}
	return c, nil
}
