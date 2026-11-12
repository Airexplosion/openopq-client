package mediaconv

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestFormatsAndSilkValidation(t *testing.T) {
	for _, v := range []string{"wav", "mp3", "amr", "wma", "m4a", "spx", "ogg", "flac"} {
		if !ValidFormat(v) {
			t.Fatal(v)
		}
	}
	for _, v := range []string{"../mp3", "-y", "MP3", ""} {
		if ValidFormat(v) {
			t.Fatal(v)
		}
	}
	if _, _, e := Encode(context.Background(), []byte("\x02#!SILK_V3\xff")); e == nil {
		t.Fatal("truncated SILK accepted")
	}
}

// Set OPENOPQ_MEDIA_INTEGRATION=1 to exercise real converter executables.
func TestRealSilkRoundtripAndAllOneBotFormats(t *testing.T) {
	if os.Getenv("OPENOPQ_MEDIA_INTEGRATION") != "1" {
		t.Skip("native converter integration is opt-in")
	}
	dir := t.TempDir()
	pcm := make([]byte, 24000*2)
	for i := 0; i < 24000; i++ {
		sample := int16(math.Sin(float64(i)*2*math.Pi*440/24000) * 5000)
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(sample))
	}
	input := filepath.Join(dir, "tone.wav")
	if e := writeWAV(pcm, input); e != nil {
		t.Fatal(e)
	}
	wav, _ := os.ReadFile(input)
	silk, duration, e := Encode(context.Background(), wav)
	if e != nil || (duration < 1 || duration > 2) || !IsSilk(silk) {
		t.Fatal(duration, e)
	}
	source := filepath.Join(dir, "tone.silk")
	_ = os.WriteFile(source, silk, 0600)
	for _, format := range []string{"wav", "mp3", "amr", "wma", "m4a", "spx", "ogg", "flac"} {
		t.Run(format, func(t *testing.T) {
			out := filepath.Join(dir, "output."+format)
			if e := Convert(context.Background(), source, out, format); e != nil {
				t.Fatal(e)
			}
			st, e := os.Stat(out)
			if e != nil || st.Size() < 32 {
				t.Fatal(st, e)
			}
			data, e := os.ReadFile(out)
			if e != nil {
				t.Fatal(e)
			}
			signature := map[string][]byte{"wav": []byte("RIFF"), "amr": []byte("#!AMR"), "spx": []byte("OggS"), "ogg": []byte("OggS"), "flac": []byte("fLaC")}
			if magic, ok := signature[format]; ok && !bytes.HasPrefix(data, magic) {
				t.Fatal("wrong file format", format)
			}
			if format == "spx" && !bytes.Contains(data, []byte("Speex")) {
				t.Fatal("not a Speex stream")
			}
			if format == "m4a" && string(data[4:8]) != "ftyp" {
				t.Fatal("not an M4A file")
			}

		})
	}
}
