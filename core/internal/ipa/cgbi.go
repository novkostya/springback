package ipa

// CgBI — the reason an icon lifted out of an .ipa is not a PNG you can serve.
//
// Every image in an App Store build has been through Apple's fork of pngcrush ("-iphone"), which
// produces a file that keeps the PNG magic number and almost nothing else about the format:
//
//   1. A `CgBI` chunk is inserted before IHDR. Its name begins with a capital letter, which in
//      PNG means CRITICAL — a decoder that does not know the chunk is REQUIRED to refuse the
//      file. Go's image/png does exactly that, so this never even reaches the pixels.
//   2. IDAT holds a RAW DEFLATE stream: the two-byte zlib header and the trailing adler32 are
//      stripped. Feeding it to zlib fails on the first byte.
//   3. The channels are stored BGRA, not RGBA.
//   4. RGB is PREMULTIPLIED by alpha.
//
// Undoing 3 and 4 without undoing 1 and 2 gets you nowhere, and undoing 1 and 2 without 3 and 4
// gets you an icon with the red and blue swapped — which looks plausible enough on a lot of app
// icons to ship unnoticed. Hence one function that does all four.
//
// The rebuild deliberately does NOT implement PNG's scanline unfiltering. It re-wraps the
// inflated bytes in a real zlib stream, emits a minimal standard PNG, and hands that to
// image/png. Unfiltering is the fiddly part of a PNG decoder (five filter types, byte offsets
// that depend on the pixel width) and the standard library already has a correct one; there is
// no reason for a second implementation to exist in this repo.

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"io"
)

var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

type pngChunk struct {
	typ  string
	data []byte
}

// normalizePNG returns a PNG that any decoder will accept.
//
// A file with no CgBI chunk is returned UNTOUCHED rather than re-encoded. Most of these come
// straight out of an archive and are already valid; re-encoding them would spend CPU to produce
// a byte-for-byte different file with identical pixels, and would silently drop colour profiles.
func normalizePNG(b []byte) ([]byte, error) {
	chunks, err := splitPNG(b)
	if err != nil {
		return nil, err
	}

	var ihdr, idat []byte
	cgbi := false
	for _, c := range chunks {
		switch c.typ {
		case "CgBI":
			cgbi = true
		case "IHDR":
			ihdr = c.data
		case "IDAT":
			idat = append(idat, c.data...)
		}
	}
	if !cgbi {
		return b, nil
	}
	if len(ihdr) < 13 {
		return nil, errors.New("cgbi png: missing or short IHDR")
	}
	if len(idat) == 0 {
		return nil, errors.New("cgbi png: no IDAT")
	}

	width := int(binary.BigEndian.Uint32(ihdr[0:4]))
	height := int(binary.BigEndian.Uint32(ihdr[4:8]))
	depth, colorType, interlace := ihdr[8], ihdr[9], ihdr[12]

	// Interlaced CgBI has never been observed — pngcrush -iphone does not produce it — and the
	// scanline arithmetic below (used only as a length sanity check) assumes it away. Refuse
	// rather than emit something subtly wrong.
	if interlace != 0 {
		return nil, fmt.Errorf("cgbi png: interlaced (%d), unsupported", interlace)
	}

	raw, err := inflateRaw(idat, expectedRawLen(width, height, depth, colorType))
	if err != nil {
		return nil, fmt.Errorf("cgbi png: inflate IDAT: %w", err)
	}

	var zbuf bytes.Buffer
	zw := zlib.NewWriter(&zbuf)
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}

	// Only IHDR/IDAT/IEND survive. Apple also emits an `iDOT` chunk (an index used to decode
	// halves of an image in parallel) whose offsets refer to the ORIGINAL stream; carrying it
	// onto a re-compressed IDAT would describe a file that no longer exists.
	var rebuilt bytes.Buffer
	rebuilt.Write(pngMagic)
	writeChunk(&rebuilt, "IHDR", ihdr)
	writeChunk(&rebuilt, "IDAT", zbuf.Bytes())
	writeChunk(&rebuilt, "IEND", nil)

	img, err := png.Decode(&rebuilt)
	if err != nil {
		return nil, fmt.Errorf("cgbi png: decode rebuilt: %w", err)
	}

	// The swap is only defined for 8-bit truecolour+alpha, which is what every icon measured so
	// far is. Anything else is returned as decoded: possibly wrong in the colour channels, but a
	// real image, and better than no icon at all.
	if nr, ok := img.(*image.NRGBA); ok && colorType == 6 && depth == 8 {
		unswizzle(nr.Pix)
	}

	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.DefaultCompression}).Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// unswizzle turns Apple's premultiplied BGRA back into straight RGBA, in place.
//
// image/png handed us these bytes believing them to be R,G,B,A; they are actually B,G,R,A with
// the colour channels already multiplied by A. Both wrongs are undone in one pass.
func unswizzle(pix []byte) {
	for i := 0; i+3 < len(pix); i += 4 {
		b, g, r, a := pix[i], pix[i+1], pix[i+2], pix[i+3]
		pix[i] = unpremultiply(r, a)
		pix[i+1] = unpremultiply(g, a)
		pix[i+2] = unpremultiply(b, a)
	}
}

func unpremultiply(c, a byte) byte {
	if a == 0 {
		return 0
	}
	if a == 0xff {
		return c
	}
	v := int(c) * 0xff / int(a)
	if v > 0xff {
		// Rounding in the original premultiply can push a channel just past its alpha.
		return 0xff
	}
	return byte(v)
}

// inflateRaw inflates a headerless deflate stream.
//
// A truncated tail is TOLERATED when the caller got the bytes it was expecting. Apple's crusher
// routinely omits the final-block marker, so a strictly correct reader reports
// io.ErrUnexpectedEOF on a stream that has already produced every pixel of the image. Treating
// that as a failure would reject most icons; ignoring the error entirely would accept genuinely
// truncated ones, so the length is what decides.
func inflateRaw(idat []byte, want int) ([]byte, error) {
	fr := flate.NewReader(bytes.NewReader(idat))
	defer func() { _ = fr.Close() }()

	raw, err := io.ReadAll(fr)
	if err != nil {
		if want > 0 && len(raw) >= want && (errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)) {
			return raw[:want], nil
		}
		return nil, err
	}
	return raw, nil
}

// expectedRawLen is the size of the filtered scanline data: one filter byte per row, then the
// row's pixels. Returns 0 for combinations it cannot compute, which disables the length check.
func expectedRawLen(width, height int, depth, colorType byte) int {
	var channels int
	switch colorType {
	case 0:
		channels = 1
	case 2:
		channels = 3
	case 4:
		channels = 2
	case 6:
		channels = 4
	default:
		return 0
	}
	if width <= 0 || height <= 0 || depth == 0 {
		return 0
	}
	bitsPerRow := width * channels * int(depth)
	return height * (1 + (bitsPerRow+7)/8)
}

// splitPNG walks the chunk stream. CRCs are not verified: these files come out of a zip that has
// already checksummed them, and a CgBI file's CRCs are correct for bytes we are about to throw
// away regardless.
func splitPNG(b []byte) ([]pngChunk, error) {
	if len(b) < len(pngMagic) || !bytes.Equal(b[:len(pngMagic)], pngMagic) {
		return nil, errors.New("not a png")
	}
	var out []pngChunk
	off := len(pngMagic)
	for off+8 <= len(b) {
		n := int(binary.BigEndian.Uint32(b[off : off+4]))
		typ := string(b[off+4 : off+8])
		start := off + 8
		if n < 0 || start+n+4 > len(b) {
			return nil, fmt.Errorf("png: chunk %q overruns the file", typ)
		}
		out = append(out, pngChunk{typ: typ, data: b[start : start+n]})
		off = start + n + 4
		if typ == "IEND" {
			break
		}
	}
	if len(out) == 0 {
		return nil, errors.New("png: no chunks")
	}
	return out, nil
}

func writeChunk(w *bytes.Buffer, typ string, data []byte) {
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(len(data)))
	copy(hdr[4:8], typ)
	w.Write(hdr[:])
	w.Write(data)

	crc := crc32.NewIEEE()
	_, _ = crc.Write(hdr[4:8])
	_, _ = crc.Write(data)
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], crc.Sum32())
	w.Write(sum[:])
}
