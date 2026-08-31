//go:build online

package komiku

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestOnlineDiscoverAndDownloadOneKomikuImage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client := NewClient(nil, 200*time.Millisecond)
	chapters, err := client.Discover(ctx, "https://komiku.org/manga/sousou-no-frieren/")
	if err != nil {
		t.Fatal(err)
	}
	if len(chapters) == 0 {
		t.Fatal("online discovery returned no chapters")
	}
	images, err := client.ChapterImages(ctx, chapters[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) == 0 {
		t.Fatal("online chapter returned no images")
	}
	var format ImageFormat
	err = client.DownloadImage(ctx, images[0], chapters[0].URL, func(_ string, body io.Reader) error {
		var header [12]byte
		n, err := io.ReadFull(body, header[:])
		if err != nil && err != io.ErrUnexpectedEOF {
			return err
		}
		format, _ = DetectImage(header[:n])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if format == "" {
		t.Fatal("online image lacked accepted magic bytes")
	}
}
