package imaging

import (
	"image"
	"math"
)

// linearLUTSize is the number of entries in the linear-to-sRGB lookup table.
// 8192 entries keep the round-trip error well below 1/255 for every 8-bit
// sRGB value (see TestSRGBLUTRoundTrip).
const linearLUTSize = 8192

// Lookup tables for sRGB <-> linear-light conversion. They are built once at
// package init and are read-only afterwards, so they are safe for concurrent
// use by the parallel resampling workers.
var (
	// srgbToLinearLUT maps an 8-bit sRGB value to linear light in [0, 1].
	srgbToLinearLUT [256]float64
	// linearToSrgbLUT maps a linear-light value in [0, 1] (quantized to
	// linearLUTSize-1 steps) back to an 8-bit sRGB value.
	linearToSrgbLUT [linearLUTSize]uint8
)

func init() {
	for i := 0; i < 256; i++ {
		srgbToLinearLUT[i] = srgbToLinear(float64(i) / 255)
	}
	for i := 0; i < linearLUTSize; i++ {
		linearToSrgbLUT[i] = clamp(linearToSrgb(float64(i)/(linearLUTSize-1)) * 255)
	}
}

// srgbToLinear converts a single sRGB-encoded channel value in [0, 1] to
// linear light using the sRGB transfer function.
func srgbToLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// linearToSrgb converts a single linear-light channel value in [0, 1] back to
// the sRGB encoding.
func linearToSrgb(c float64) float64 {
	if c <= 0.0031308 {
		return 12.92 * c
	}
	return 1.055*math.Pow(c, 1.0/2.4) - 0.055
}

// linearToSrgb8 converts a linear-light value in [0, 1] to an 8-bit sRGB
// value using the lookup table. Out-of-range values (possible with
// negative-lobe filters such as Lanczos) are clamped.
func linearToSrgb8(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return linearToSrgbLUT[int(v*(linearLUTSize-1)+0.5)]
}

// LinearLight returns a copy of the given resampling filter that performs the
// resampling convolution in linear-light space instead of the sRGB-encoded
// space. Resampling in linear light avoids the darkening artifacts that
// appear when averaging sRGB-encoded values directly (most visible as dark
// edges and an overall dark shift when downscaling high-contrast images).
//
// The option is carried by the filter value, so it works with Resize, Fit,
// Fill and Thumbnail without changing their signatures:
//
//	dstImage := imaging.Resize(srcImage, 800, 600, imaging.LinearLight(imaging.Lanczos))
//
// sRGB <-> linear conversion uses lookup tables, so the overhead over the
// default path is small. The option has no effect with the NearestNeighbor
// filter (no convolution is performed).
//
// For images with transparency, combine with PremultipliedAlpha so that
// colors of (semi-)transparent pixels do not bleed into the result:
//
//	filter := imaging.LinearLight(imaging.PremultipliedAlpha(imaging.Lanczos))
func LinearLight(filter ResampleFilter) ResampleFilter {
	filter.linearLight = true
	return filter
}

// PremultipliedAlpha returns a copy of the given resampling filter that
// premultiplies the color channels by alpha before the convolution and
// unpremultiplies the result afterwards. This prevents semi-transparent and
// fully transparent pixels from leaking their (usually meaningless) color
// into opaque areas, which shows up as dark or colored fringes along
// transparency edges.
//
// It is most useful together with LinearLight:
//
//	filter := imaging.LinearLight(imaging.PremultipliedAlpha(imaging.Lanczos))
//
// Used alone (without LinearLight) it is equivalent to the default resampling
// path, which already performs alpha-weighted accumulation in sRGB space, and
// is routed to it.
func PremultipliedAlpha(filter ResampleFilter) ResampleFilter {
	filter.premultiplyAlpha = true
	return filter
}

// resizeHorizontalLinear is the linear-light counterpart of resizeHorizontal.
// The scanline is converted to linear light once per row (lookup table) and
// the convolution then runs on the linear values.
func resizeHorizontalLinear(img image.Image, width int, filter ResampleFilter) *image.NRGBA {
	src := newScanner(img)
	dst := image.NewNRGBA(image.Rect(0, 0, width, src.h))
	weights := precomputeWeights(width, src.w, filter)
	premul := filter.premultiplyAlpha
	parallel(0, src.h, func(ys <-chan int) {
		scanLine := make([]uint8, src.w*4)
		linear := make([]float64, src.w*4)
		for y := range ys {
			src.scan(0, y, src.w, y+1, scanLine)
			for i := 0; i < src.w*4; i += 4 {
				linear[i+0] = srgbToLinearLUT[scanLine[i+0]]
				linear[i+1] = srgbToLinearLUT[scanLine[i+1]]
				linear[i+2] = srgbToLinearLUT[scanLine[i+2]]
				linear[i+3] = float64(scanLine[i+3])
			}
			j0 := y * dst.Stride
			for x := range weights {
				var r, g, b, a float64
				if premul {
					for _, w := range weights[x] {
						i := w.index * 4
						aw := linear[i+3] * w.weight
						r += linear[i+0] * aw
						g += linear[i+1] * aw
						b += linear[i+2] * aw
						a += aw
					}
					if a != 0 {
						aInv := 1 / a
						j := j0 + x*4
						d := dst.Pix[j : j+4 : j+4]
						d[0] = linearToSrgb8(r * aInv)
						d[1] = linearToSrgb8(g * aInv)
						d[2] = linearToSrgb8(b * aInv)
						d[3] = clamp(a)
					}
				} else {
					for _, w := range weights[x] {
						i := w.index * 4
						r += linear[i+0] * w.weight
						g += linear[i+1] * w.weight
						b += linear[i+2] * w.weight
						a += linear[i+3] * w.weight
					}
					j := j0 + x*4
					d := dst.Pix[j : j+4 : j+4]
					d[0] = linearToSrgb8(r)
					d[1] = linearToSrgb8(g)
					d[2] = linearToSrgb8(b)
					d[3] = clamp(a)
				}
			}
		}
	})
	return dst
}

// resizeVerticalLinear is the linear-light counterpart of resizeVertical.
func resizeVerticalLinear(img image.Image, height int, filter ResampleFilter) *image.NRGBA {
	src := newScanner(img)
	dst := image.NewNRGBA(image.Rect(0, 0, src.w, height))
	weights := precomputeWeights(height, src.h, filter)
	premul := filter.premultiplyAlpha
	parallel(0, src.w, func(xs <-chan int) {
		scanLine := make([]uint8, src.h*4)
		linear := make([]float64, src.h*4)
		for x := range xs {
			src.scan(x, 0, x+1, src.h, scanLine)
			for i := 0; i < src.h*4; i += 4 {
				linear[i+0] = srgbToLinearLUT[scanLine[i+0]]
				linear[i+1] = srgbToLinearLUT[scanLine[i+1]]
				linear[i+2] = srgbToLinearLUT[scanLine[i+2]]
				linear[i+3] = float64(scanLine[i+3])
			}
			for y := range weights {
				var r, g, b, a float64
				if premul {
					for _, w := range weights[y] {
						i := w.index * 4
						aw := linear[i+3] * w.weight
						r += linear[i+0] * aw
						g += linear[i+1] * aw
						b += linear[i+2] * aw
						a += aw
					}
					if a != 0 {
						aInv := 1 / a
						j := y*dst.Stride + x*4
						d := dst.Pix[j : j+4 : j+4]
						d[0] = linearToSrgb8(r * aInv)
						d[1] = linearToSrgb8(g * aInv)
						d[2] = linearToSrgb8(b * aInv)
						d[3] = clamp(a)
					}
				} else {
					for _, w := range weights[y] {
						i := w.index * 4
						r += linear[i+0] * w.weight
						g += linear[i+1] * w.weight
						b += linear[i+2] * w.weight
						a += linear[i+3] * w.weight
					}
					j := y*dst.Stride + x*4
					d := dst.Pix[j : j+4 : j+4]
					d[0] = linearToSrgb8(r)
					d[1] = linearToSrgb8(g)
					d[2] = linearToSrgb8(b)
					d[3] = clamp(a)
				}
			}
		}
	})
	return dst
}
