// Package imgview renders images inline in a terminal that supports the
// Kitty graphics protocol (Ghostty, Kitty, WezTerm).
package imgview

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"  // register GIF decoding with image.Decode
	_ "image/jpeg" // register JPEG decoding with image.Decode
	"image/png"    // also registers PNG decoding with image.Decode
	"io"
	"net/http"

	"github.com/BourgeoisBear/rasterm"
	_ "golang.org/x/image/webp" // register WebP decoding with image.Decode
)

// kittyChunkSize is the max bytes of base64 payload per escape sequence,
// per the Kitty graphics protocol spec.
const kittyChunkSize = 4096

// Fetch downloads an attachment (Airtable attachment URLs are pre-signed
// and public, so no auth header is needed).
func Fetch(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetching image: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// assumedCellAspect is a typical terminal cell's width-to-height ratio in
// pixels (cells are roughly twice as tall as they are wide in most
// monospace fonts). We don't know the real value for the user's terminal,
// only their fonts, but we don't need to: this is only used to decide
// which single dimension to hand to Kitty -- the terminal itself computes
// the other one from the image's actual pixel size, so any imprecision
// here just shifts which axis binds, not the resulting aspect ratio.
const assumedCellAspect = 0.5

// Render decodes image bytes and encodes them as a Kitty graphics protocol
// escape sequence, scaled to fit within maxCols x maxRows terminal cells
// without distorting the image's aspect ratio. Per the Kitty protocol,
// specifying both c= and r= stretches the image to exactly that box; we
// avoid that by only ever specifying whichever single dimension is the
// tighter constraint, letting the terminal derive the other from the
// image's real pixel dimensions.
func Render(data []byte, maxCols, maxRows uint32) (string, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("decoding image: %w", err)
	}

	bounds := img.Bounds()
	imgAspect := float64(bounds.Dx()) / float64(bounds.Dy())
	boxAspect := float64(maxCols) * assumedCellAspect / float64(maxRows)

	opts := rasterm.KittyImgOpts{}
	if imgAspect > boxAspect {
		opts.DstCols = maxCols // width is the binding constraint
	} else {
		opts.DstRows = maxRows // height is the binding constraint
	}

	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		return "", fmt.Errorf("re-encoding image as PNG: %w", err)
	}

	var out bytes.Buffer
	if err := writeKittyImage(&out, pngBuf.Bytes(), opts); err != nil {
		return "", fmt.Errorf("encoding image: %w", err)
	}
	return out.String(), nil
}

// writeKittyImage writes pngData as a chunked Kitty graphics protocol
// sequence, in "quiet" mode (q=2). rasterm's own KittyWriteImage doesn't
// support suppressing the terminal's confirmation response, and that
// response -- an escape sequence the terminal sends back on stdin after
// every image command -- collides with Bubbletea reading keyboard input,
// which is what causes stuck/unresponsive keys after showing an image.
func writeKittyImage(out io.Writer, pngData []byte, opts rasterm.KittyImgOpts) error {
	b64 := base64.StdEncoding.EncodeToString(pngData)

	for i := 0; len(b64) > 0; i++ {
		n := kittyChunkSize
		if n > len(b64) {
			n = len(b64)
		}
		chunk := b64[:n]
		b64 = b64[n:]

		more := "0"
		if len(b64) > 0 {
			more = "1"
		}

		var header string
		if i == 0 {
			// Only the first chunk carries the actual command: transmit
			// (a=T) + display, PNG format (f=100), base64-in-band (t=d),
			// quiet (q=2), plus this chunk's "more data follows" flag.
			header = opts.ToHeader("a=T", "f=100", "t=d", "q=2", "m="+more)
		} else {
			header = rasterm.KITTY_IMG_HDR + "m=" + more + ";"
		}

		if _, err := fmt.Fprint(out, header, chunk, rasterm.KITTY_IMG_FTR); err != nil {
			return err
		}
	}
	return nil
}
