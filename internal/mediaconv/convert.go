// Package mediaconv runs bounded, on-demand audio conversion. No converter stays
// resident. Codec subprocesses do not inherit signing credentials in their environment.
package mediaconv

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var formats = map[string][]string{
	"wav": {"-c:a", "pcm_s16le", "-f", "wav"}, "mp3": {"-c:a", "libmp3lame", "-f", "mp3"}, "amr": {"-ar", "8000", "-c:a", "libopencore_amrnb", "-f", "amr"}, "wma": {"-c:a", "wmav2", "-f", "asf"}, "m4a": {"-c:a", "aac", "-f", "ipod"}, "spx": {"-ar", "16000", "-c:a", "libspeex", "-f", "ogg"}, "ogg": {"-c:a", "libvorbis", "-f", "ogg"}, "flac": {"-c:a", "flac", "-f", "flac"},
}

func ValidFormat(s string) bool { _, ok := formats[s]; return ok }
func tool(name string) (string, error) {
	if p := os.Getenv("OPENOPQ_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))); p != "" {
		return p, nil
	}
	exe, _ := os.Executable()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	for _, dir := range []string{filepath.Join(filepath.Dir(exe), "tools"), filepath.Dir(exe)} {
		p := filepath.Join(dir, name+suffix)
		if st, e := os.Stat(p); e == nil && !st.IsDir() {
			return p, nil
		}
	}
	p, e := exec.LookPath(name)
	if e != nil {
		return "", fmt.Errorf("%s is required: install media tools beside openopq or set OPENOPQ_%s", name, strings.ToUpper(strings.ReplaceAll(name, "-", "_")))
	}
	return p, nil
}

var slots = make(chan struct{}, 2)

func run(ctx context.Context, name string, args ...string) error {
	p, e := tool(name)
	if e != nil {
		return e
	}
	cmd := exec.CommandContext(ctx, p, args...)
	cmd.Env = []string{} // Do not inherit bot secrets from environment.
	// Some platform runtimes require their system directory and temporary path.
	for _, k := range []string{"SystemRoot", "WINDIR", "TEMP", "TMP", "PATH", "HOME"} {
		if v := os.Getenv(k); v != "" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	var stderr limitedBuffer
	cmd.Stdout = &stderr
	cmd.Stderr = &stderr
	if e = cmd.Run(); e != nil {
		return fmt.Errorf("%s conversion failed: %w (%s)", name, e, stderr.String())
	}
	return nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < 4096 {
		take := 4096 - b.Len()
		if take > len(p) {
			take = len(p)
		}
		_, _ = b.Buffer.Write(p[:take])
	}
	return n, nil
}
func IsSilk(b []byte) bool {
	return bytes.HasPrefix(b, []byte("#!SILK_V3")) || bytes.HasPrefix(b, []byte("\x02#!SILK_V3"))
}
func writeWAV(pcm []byte, path string) error {
	if len(pcm) == 0 || len(pcm) > 30<<20 {
		return fmt.Errorf("invalid decoded PCM size")
	}
	h := make([]byte, 44)
	copy(h, "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(len(pcm)+36))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 1)
	binary.LittleEndian.PutUint32(h[24:], 24000)
	binary.LittleEndian.PutUint32(h[28:], 48000)
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(pcm)))
	return os.WriteFile(path, append(h, pcm...), 0600)
}
func Convert(ctx context.Context, input, output, format string) error {
	if !ValidFormat(format) {
		return fmt.Errorf("invalid audio format")
	}
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	dir, e := TempDir("convert-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	data, e := os.ReadFile(input)
	if e != nil {
		return e
	}
	if len(data) > 30<<20 {
		return fmt.Errorf("audio exceeds 30 MiB")
	}
	source := input
	if IsSilk(data) {
		if _, _, e = Encode(ctx, data); e != nil {
			return e
		}

		source = filepath.Join(dir, "decoded.wav")
		if e = run(ctx, "silk", "decode", input, "--output", source, "--to", "wav"); e != nil {
			return e
		}
		if format == "wav" {
			data, e = os.ReadFile(source)
			if e != nil {
				return e
			}
			if len(data) == 0 || len(data) > 30<<20 {
				return fmt.Errorf("invalid decoded audio size")
			}
			return os.WriteFile(output, data, 0600)
		}

	}
	tmp := filepath.Join(dir, "result."+format)
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-protocol_whitelist", "file,pipe", "-format_whitelist", "wav,mp3,amr,ogg,flac,mov,asf,aac", "-i", source, "-vn", "-ac", "1", "-t", "300"}
	args = append(args, formats[format]...)
	args = append(args, tmp)
	if e = run(ctx, "ffmpeg", args...); e != nil {
		return e
	}
	data, e = os.ReadFile(tmp)
	if e != nil {
		return e
	}
	if len(data) == 0 || len(data) > 30<<20 {
		return fmt.Errorf("converted audio is empty or too large")
	}
	return os.WriteFile(output, data, 0600)
}

// Encode accepts ordinary audio, resamples it and returns Tencent SILK for upload.
func Encode(ctx context.Context, data []byte) ([]byte, uint32, error) {
	if len(data) == 0 || len(data) > 30<<20 {
		return nil, 0, fmt.Errorf("invalid voice size")
	}
	if IsSilk(data) {
		offset := 9
		if data[0] == 2 {
			offset = 10
		}
		frames := 0
		for offset < len(data) {
			if offset+2 > len(data) {
				return nil, 0, fmt.Errorf("truncated SILK")
			}
			n := int(binary.LittleEndian.Uint16(data[offset:]))
			offset += 2
			if n == 65535 {
				break
			}
			if n > len(data)-offset {
				return nil, 0, fmt.Errorf("invalid SILK frame")
			}
			offset += n
			frames++
		}
		if frames == 0 || frames > 15000 {
			return nil, 0, fmt.Errorf("invalid SILK duration")
		}
		return data, uint32((frames*20 + 999) / 1000), nil
	}
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	dir, e := TempDir("encode-")
	if e != nil {
		return nil, 0, e
	}
	defer os.RemoveAll(dir)
	input, pcm, out := filepath.Join(dir, "input"), filepath.Join(dir, "input.wav"), filepath.Join(dir, "output.silk")
	if e = os.WriteFile(input, data, 0600); e != nil {
		return nil, 0, e
	}
	if e = run(ctx, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-protocol_whitelist", "file,pipe", "-format_whitelist", "wav,mp3,amr,ogg,flac,mov,asf,aac", "-i", input, "-vn", "-ar", "24000", "-ac", "1", "-t", "300", "-c:a", "pcm_s16le", "-f", "wav", pcm); e != nil {
		return nil, 0, e
	}
	st, e := os.Stat(pcm)
	if e != nil {
		return nil, 0, e
	}
	if st.Size() == 0 || st.Size() > 30<<20 {
		return nil, 0, fmt.Errorf("invalid voice duration")
	}
	if e = run(ctx, "silk", "encode", pcm, "--output", out, "--sample-rate", "24000"); e != nil {
		return nil, 0, e
	}
	b, e := os.ReadFile(out)
	return b, uint32((st.Size() + 47999) / 48000), e
}

// TempDir uses the application cache rather than an inherited system Temp ACL.
// Every owner gets a new private directory and callers remove their own directory.
func TempDir(prefix string) (string, error) {
	base := os.Getenv("OPENOPQ_MEDIA_CACHE")
	if base == "" {
		root, e := os.UserCacheDir()
		if e != nil {
			return "", e
		}
		base = filepath.Join(root, "OpenOPQ", "media")
	}
	base, e := filepath.Abs(base)
	if e != nil {
		return "", e
	}
	if e = os.MkdirAll(base, 0700); e != nil {
		return "", e
	}
	return os.MkdirTemp(base, prefix)
}
