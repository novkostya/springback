package ipa

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"image"
	"image/color"
	"image/png"
	"io"
	"testing"
)

// TestNormalizePNGUndoesCgBI builds a CgBI file from a known image and checks the pixels survive
// the round trip.
//
// The fixture is SYNTHESISED rather than committed. Real CgBI files are App Store app icons —
// someone else's trademarked artwork, which has no business being checked into this repo — and a
// generated one exercises the same four transformations while letting the test assert exact
// colours instead of "looks about right".
//
// The colours are chosen to catch the failure this code exists to prevent: pure red and pure
// blue are indistinguishable under a channel swap unless the test looks at them, and a
// half-transparent pixel is the only thing that catches premultiplication being skipped.
func TestNormalizePNGUndoesCgBI(t *testing.T) {
	want := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	want.SetNRGBA(0, 0, color.NRGBA{R: 255, G: 0, B: 0, A: 255}) // red, opaque
	want.SetNRGBA(1, 0, color.NRGBA{R: 0, G: 0, B: 255, A: 255}) // blue, opaque
	want.SetNRGBA(0, 1, color.NRGBA{R: 0, G: 255, B: 0, A: 128}) // green, half alpha
	want.SetNRGBA(1, 1, color.NRGBA{R: 255, G: 255, B: 0, A: 0}) // fully transparent

	got, err := normalizePNG(makeCgBI(t, want))
	if err != nil {
		t.Fatalf("normalizePNG: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}

	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			w := want.NRGBAAt(x, y)
			g := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			if w.A == 0 {
				// A transparent pixel's colour channels are unrecoverable: premultiplying
				// by zero destroyed them. Only the alpha is asserted.
				if g.A != 0 {
					t.Errorf("(%d,%d) alpha = %d, want 0", x, y, g.A)
				}
				continue
			}
			// Premultiply-then-unpremultiply is lossy by up to one part in alpha, so an
			// exact match is not available for the half-transparent pixel.
			if !nearNRGBA(g, w, 2) {
				t.Errorf("(%d,%d) = %+v, want %+v", x, y, g, w)
			}
		}
	}
}

// TestNormalizePNGPassesThroughStandardFiles checks a plain PNG comes back byte-identical: most
// files this touches are already valid and must not be re-encoded.
func TestNormalizePNGPassesThroughStandardFiles(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	src.SetNRGBA(0, 0, color.NRGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}

	got, err := normalizePNG(buf.Bytes())
	if err != nil {
		t.Fatalf("normalizePNG: %v", err)
	}
	if !bytes.Equal(got, buf.Bytes()) {
		t.Errorf("standard png was rewritten (%d -> %d bytes); it should pass through untouched", buf.Len(), len(got))
	}
}

func TestNormalizePNGRejectsNonPNG(t *testing.T) {
	if _, err := normalizePNG([]byte("this is not a png")); err == nil {
		t.Error("want an error for non-png input, got nil")
	}
}

func TestSizeInName(t *testing.T) {
	cases := map[string]int{
		"AppIcon60x60@2x.png":                120,
		"AppIcon76x76@2x~ipad.png":           152,
		"ToastmasterMajorSymbol60x60@3x.png": 180,
		"AppIcon.png":                        0,
		"Icon-Small.png":                     0,
		"AppIcon40x40.png":                   40,
	}
	for name, want := range cases {
		if got := sizeInName(name); got != want {
			t.Errorf("sizeInName(%q) = %d, want %d", name, got, want)
		}
	}
}

func nearNRGBA(a, b color.NRGBA, tol int) bool {
	d := func(x, y byte) int {
		if x > y {
			return int(x) - int(y)
		}
		return int(y) - int(x)
	}
	return d(a.R, b.R) <= tol && d(a.G, b.G) <= tol && d(a.B, b.B) <= tol && d(a.A, b.A) <= tol
}

// makeCgBI applies the four transformations pngcrush -iphone applies, so the test has something
// to undo: premultiply alpha, swap R and B, strip the zlib wrapper off IDAT, insert CgBI.
func makeCgBI(t *testing.T, src *image.NRGBA) []byte {
	t.Helper()

	// Premultiply and swap into the byte order Apple stores.
	mangled := image.NewNRGBA(src.Bounds())
	copy(mangled.Pix, src.Pix)
	for i := 0; i+3 < len(mangled.Pix); i += 4 {
		r, g, b, a := mangled.Pix[i], mangled.Pix[i+1], mangled.Pix[i+2], mangled.Pix[i+3]
		pm := func(c byte) byte { return byte(int(c) * int(a) / 0xff) }
		mangled.Pix[i], mangled.Pix[i+1], mangled.Pix[i+2] = pm(b), pm(g), pm(r)
	}

	var enc bytes.Buffer
	if err := png.Encode(&enc, mangled); err != nil {
		t.Fatal(err)
	}
	chunks, err := splitPNG(enc.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	var ihdr, idat []byte
	for _, c := range chunks {
		switch c.typ {
		case "IHDR":
			ihdr = c.data
		case "IDAT":
			idat = append(idat, c.data...)
		}
	}

	// Unwrap the zlib stream back to raw deflate, which is what CgBI's IDAT holds.
	zr, err := zlib.NewReader(bytes.NewReader(idat))
	if err != nil {
		t.Fatal(err)
	}
	rawPixels, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	var deflated bytes.Buffer
	fw, err := flate.NewWriter(&deflated, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(rawPixels); err != nil {
		t.Fatal(err)
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	out.Write(pngMagic)
	writeChunk(&out, "CgBI", []byte{0x50, 0x00, 0x20, 0x02})
	writeChunk(&out, "IHDR", ihdr)
	writeChunk(&out, "IDAT", deflated.Bytes())
	writeChunk(&out, "IEND", nil)
	return out.Bytes()
}
