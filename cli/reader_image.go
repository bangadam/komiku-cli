// reader_image.go validates cached reader images with bounded decoding and checksums.
package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"

	_ "golang.org/x/image/webp"

	"github.com/bangadam/komiku-cli/kiryuu"
)

type readerContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r readerContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// Validate supported image types, bounded dimensions, decoding and checksum.
// Unlike ValidResumeFile, legitimate small images are reusable; magic alone
// cannot distinguish an interrupted download from a usable image.
func readerImageInfo(ctx context.Context, file *os.File) (int64, string, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, "", err
	}
	if info.Size() <= 12 || info.Size() > maxReaderImage {
		return 0, "", errors.New("invalid image size")
	}
	var header [12]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return 0, "", err
	}
	if _, valid := kiryuu.DetectImage(header[:]); !valid {
		return 0, "", errors.New("invalid image payload")
	}
	hash := sha256.New()
	hash.Write(header[:])
	n, err := io.Copy(hash, io.LimitReader(readerContextReader{ctx, file}, maxReaderImage+1))
	if err != nil {
		return 0, "", err
	}
	if n+12 != info.Size() {
		return 0, "", errors.New("image size changed while reading")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, "", err
	}
	config, _, err := image.DecodeConfig(readerContextReader{ctx, file})
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > 32_000_000 || config.Height > 32_000_000/config.Width {
		return 0, "", errors.New("invalid image header or dimensions")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, "", err
	}
	if _, _, err := image.Decode(readerContextReader{ctx, file}); err != nil {
		return 0, "", fmt.Errorf("corrupt or truncated image: %w", err)
	}
	return info.Size(), hex.EncodeToString(hash.Sum(nil)), ctx.Err()
}
