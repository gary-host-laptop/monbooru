//go:build tagger

package tagger

import (
	"fmt"
	"image"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
	"golang.org/x/image/draw"
)

// inputTensorPools holds one float32-slice sync.Pool per model input
// size. buildTensor writes every element of the returned slice so a
// recycled buffer needs no zeroing; ort.NewTensor aliases the slice in
// place, and callers must put the buffer back only after Destroy on
// the tensor.
var inputTensorPools sync.Map

// The pool holds *[]float32, not []float32: pooling the slice itself boxes
// its header into the interface on every Put, which allocates.
func acquireTensor(size int) []float32 {
	if p, ok := inputTensorPools.Load(size); ok {
		return *p.(*sync.Pool).Get().(*[]float32)
	}
	n := size
	fresh := &sync.Pool{New: func() any { buf := make([]float32, 3*n*n); return &buf }}
	actual, _ := inputTensorPools.LoadOrStore(size, fresh)
	return *actual.(*sync.Pool).Get().(*[]float32)
}

func releaseTensor(size int, buf []float32) {
	if p, ok := inputTensorPools.Load(size); ok {
		p.(*sync.Pool).Put(&buf)
	}
}

// imageNetMean / imageNetStd are the per-channel ImageNet statistics most
// EfficientNet / ResNet exports were trained with. Camie v2 documents
// these directly.
var (
	imageNetMean = [3]float32{0.485, 0.456, 0.406}
	imageNetStd  = [3]float32{0.229, 0.224, 0.225}
)

// clipMean / clipStd come from joytag's preprocess step (OpenAI CLIP
// values). Kept verbatim so the joytag inference path stays bit-identical
// across the refactor.
var (
	clipMean = [3]float32{0.48145466, 0.4578275, 0.40821073}
	clipStd  = [3]float32{0.26862954, 0.26130258, 0.27577711}
)

// camieDefaultFill is the fill colour Camie's onnx_inference.py pads with
// when the source image's aspect ratio doesn't fill the model's square
// input. Used when a profile picks pad="mean_color_aspect" without an
// explicit FillColor override.
var camieDefaultFill = [3]uint8{124, 116, 104}

// padAndResize pads src into a square according to the profile's pad
// strategy, then resizes to size×size. Returns *image.RGBA so the caller
// can read .Pix directly.
func padAndResize(src image.Image, size int, profile Profile) *image.RGBA {
	switch profile.Pad {
	case "mean_color_aspect":
		return padMeanColorAspect(src, size, profile.FillColor)
	}
	return padWhiteSquare(src, size)
}

// padWhiteSquare resizes src preserving aspect ratio so the long side is
// `size`, centres it on a `size×size` white canvas, then forces fully-
// transparent pixels (e.g. PNG corners) to opaque white so the tensor
// sees the same background regardless of source alpha.
//
// image.NewRGBA returns a zero-initialised buffer (alpha=0 everywhere),
// so draw.Src into the scaled sub-rect leaves the padding region with
// alpha=0. One final walk over alpha covers both jobs - the padding
// region and any transparent source pixel land on the same "fill white"
// branch in a single pass instead of two.
func padWhiteSquare(src image.Image, size int) *image.RGBA {
	scaled, offX, offY := resizeAspect(src, size)

	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(dst, scaled.Bounds().Add(image.Pt(offX, offY)), scaled, image.Point{}, draw.Src)
	for i := 3; i < len(dst.Pix); i += 4 {
		if dst.Pix[i] == 0 {
			dst.Pix[i-3] = 0xFF
			dst.Pix[i-2] = 0xFF
			dst.Pix[i-1] = 0xFF
			dst.Pix[i] = 0xFF
		}
	}
	return dst
}

// padMeanColorAspect resizes src preserving aspect ratio so the long side
// is `size`, then centres it on a `size×size` canvas filled with the
// profile's FillColor (defaulting to camieDefaultFill). Camie's published
// inference recipe.
func padMeanColorAspect(src image.Image, size int, fill [3]uint8) *image.RGBA {
	if fill == ([3]uint8{}) {
		fill = camieDefaultFill
	}
	scaled, offX, offY := resizeAspect(src, size)

	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	for i := 0; i < len(dst.Pix); i += 4 {
		dst.Pix[i+0] = fill[0]
		dst.Pix[i+1] = fill[1]
		dst.Pix[i+2] = fill[2]
		dst.Pix[i+3] = 0xFF
	}
	draw.Draw(dst, scaled.Bounds().Add(image.Pt(offX, offY)), scaled, image.Point{}, draw.Src)
	return dst
}

// resizeAspect scales src so its long side is size, preserving aspect
// (short side clamped to >= 1 px), and returns the centring offsets on a
// size x size canvas. Resize-first keeps peak transient allocation
// bounded by size^2 so a parallel inference burst on multi-megapixel
// sources stays inside small container caps.
func resizeAspect(src image.Image, size int) (scaled *image.RGBA, offX, offY int) {
	b := src.Bounds()
	w, h := b.Max.X-b.Min.X, b.Max.Y-b.Min.Y
	scaleW, scaleH := size, size
	if w >= h {
		scaleH = max(1, h*size/w)
	} else {
		scaleW = max(1, w*size/h)
	}
	scaled = image.NewRGBA(image.Rect(0, 0, scaleW, scaleH))
	draw.ApproxBiLinear.Scale(scaled, scaled.Bounds(), src, b, draw.Src, nil)
	return scaled, (size - scaleW) / 2, (size - scaleH) / 2
}

// buildTensor fills the supplied ORT input buffer from the resized RGBA
// image. tensor must have len >= 3*size*size; every element is
// overwritten so a recycled buffer needs no zeroing. Reading Pix
// directly skips the per-pixel image.Image interface dispatch that
// would otherwise keep the GPU idle between inferences.
//
// The branches diverge on layout (NHWC vs NCHW), channel order (BGR vs
// RGB), and per-channel normalisation (none, ImageNet, CLIP). A profile
// resolved to (NHWC, BGR, none) reproduces the WD14 path bit-for-bit;
// (NCHW, RGB, CLIP) reproduces joytag's.
func buildTensor(img *image.RGBA, tensor []float32, size int, profile Profile) (ort.Shape, error) {
	pix := img.Pix
	stride := img.Stride

	switch profile.Layout {
	case "nhwc":
		// WD14 is the only NHWC profile in tree and ships with
		// normalize="none" (sigmoid in the model handles its own scaling).
		// A future NHWC profile that needs a per-channel transform can
		// fold it in here.
		if profile.Normalize != "" && profile.Normalize != "none" {
			return nil, fmt.Errorf("buildTensor: NHWC + normalize=%q is not implemented", profile.Normalize)
		}
		bgr := profile.Channels == "bgr"
		for y := 0; y < size; y++ {
			row := pix[y*stride:]
			for x := 0; x < size; x++ {
				src := x * 4
				dst := (y*size + x) * 3
				r, g, b := row[src+0], row[src+1], row[src+2]
				if bgr {
					tensor[dst+0] = float32(b)
					tensor[dst+1] = float32(g)
					tensor[dst+2] = float32(r)
				} else {
					tensor[dst+0] = float32(r)
					tensor[dst+1] = float32(g)
					tensor[dst+2] = float32(b)
				}
			}
		}
		return ort.NewShape(1, int64(size), int64(size), 3), nil

	case "nchw":
		var mean, std [3]float32
		switch profile.Normalize {
		case "imagenet":
			mean, std = imageNetMean, imageNetStd
		case "clip":
			mean, std = clipMean, clipStd
		}
		bgr := profile.Channels == "bgr"
		plane := size * size
		for y := 0; y < size; y++ {
			row := pix[y*stride:]
			for x := 0; x < size; x++ {
				src := x * 4
				off := y*size + x
				r := float32(row[src+0])
				g := float32(row[src+1])
				b := float32(row[src+2])
				if bgr {
					r, b = b, r
				}
				switch profile.Normalize {
				case "imagenet", "clip":
					tensor[0*plane+off] = (r/255 - mean[0]) / std[0]
					tensor[1*plane+off] = (g/255 - mean[1]) / std[1]
					tensor[2*plane+off] = (b/255 - mean[2]) / std[2]
				case "div255":
					tensor[0*plane+off] = r / 255
					tensor[1*plane+off] = g / 255
					tensor[2*plane+off] = b / 255
				default:
					tensor[0*plane+off] = r
					tensor[1*plane+off] = g
					tensor[2*plane+off] = b
				}
			}
		}
		return ort.NewShape(1, 3, int64(size), int64(size)), nil
	}
	return nil, fmt.Errorf("buildTensor: unsupported layout %q", profile.Layout)
}
