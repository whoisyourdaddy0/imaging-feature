# NOTES: LinearLight / PremultipliedAlpha resampling options

## What was added

`LinearLight(filter)` and `PremultipliedAlpha(filter)` are filter modifiers:
each takes a `ResampleFilter` and returns a copy with an internal flag set
(unexported fields on `ResampleFilter`). Because the flags travel inside the
filter value, the options work with `Resize`, `Fit`, `Fill` and `Thumbnail`
(including the internal `cropAndResize`/`resizeAndCrop` paths) without
changing any exported function signature and without duplicating the function
family.

- `LinearLight`: the resampling convolution runs in linear-light space.
  Pixels are decoded from sRGB to linear before filtering and the result is
  re-encoded to sRGB afterwards.
- `PremultipliedAlpha`: color channels are premultiplied by alpha before the
  convolution and unpremultiplied after. Only meaningful together with
  `LinearLight`; alone it is routed to the default path (see below).

## Compatibility

- The default code path (`resizeHorizontal`/`resizeVertical`) is completely
  untouched; without the new options the output is byte-for-byte identical to
  the previous implementation. The pre-existing golden tests
  (`TestResizeGolden`, `TestFit`, `TestFill`, the exact-match table tests)
  verify this.
- One caveat: `ResampleFilter` gained unexported fields, so external code
  constructing it with an *unkeyed* struct literal
  (`imaging.ResampleFilter{1.0, kernel}`) would no longer compile. Keyed
  literals (`imaging.ResampleFilter{Support: 1.0, Kernel: kernel}`) are
  unaffected. Unkeyed construction was never documented and is discouraged by
  `go vet`.

## Why PremultipliedAlpha alone is a no-op

The default path already accumulates `color * alpha * weight` and divides by
the accumulated alpha — that *is* premultiplied-alpha resampling, just in
sRGB space. So `PremultipliedAlpha` without `LinearLight` is mathematically
identical to the default and is routed to it (verified byte-for-byte by
`TestLinearLightDefaultPathUnchanged`). The option exists to control
premultiplication explicitly in the linear-light path, where skipping it
(non-premultiplied linear convolution) lets the color of fully transparent
pixels bleed into the result.

## Lookup tables and precision

- sRGB -> linear: 256-entry `float64` table (exact per 8-bit input value).
- linear -> sRGB: 8192-entry `uint8` table indexed by
  `round(clamp(v,0,1) * 8191)`. The worst-case quantization error is
  1/16382 in linear light; at the steepest point of the sRGB curve (near 0,
  slope 12.92) that is < 0.5/255 in the output, so every 8-bit value
  round-trips exactly (`TestSRGBLUTRoundTrip` checks all 256).
- No `math.Pow` is called per pixel; tables are built once in `init` and are
  read-only afterwards, so they are safe for the existing parallel workers
  (verified with `-race`).

## 8-bit quantization

The whole resize pipeline (existing and new) scans the source into 8-bit
NRGBA scanlines (`scanner.go`), so 16-bit inputs (`NRGBA64`, `RGBA64`,
`Gray16`) are reduced to 8 bits before filtering — this is pre-existing
behavior, unchanged. Consequences for the linear-light path:

- The linear convolution operates on values quantized to 256 sRGB steps.
  For solid-color regions the round trip is exact (error 0); for general
  content the extra error vs. a full-float pipeline is below 1/255 and
  invisible in practice.
- Output is `*image.NRGBA` (8-bit), same as the default path.
- Premultiplied `RGBA`/`RGBA64` sources are unpremultiplied to NRGBA by the
  scanner (with the usual 8-bit rounding) before filtering, exactly as in the
  default path.

## Performance

The scanline is converted to linear once per row/column into a reusable
`float64` buffer; the convolution then reads floats directly instead of
converting bytes per tap, which offsets most of the LUT cost. Measured on
an Intel i7-9750H (go1.26.1, darwin/amd64), median of 3 runs of
`go test -bench ResizeLinearLight -run ^$ -count 3`:

```
BenchmarkResizeLinearLight/Down_Lanczos_PNG_Default-12                        1476962 ns/op
BenchmarkResizeLinearLight/Down_Lanczos_PNG_LinearLight-12                    1607839 ns/op   (+9%)
BenchmarkResizeLinearLight/Down_Lanczos_PNG_LinearLightPremultipliedAlpha-12  1734819 ns/op   (+17%)
BenchmarkResizeLinearLight/Up_Lanczos_PNG_Default-12                          8842152 ns/op
BenchmarkResizeLinearLight/Up_Lanczos_PNG_LinearLight-12                      8114457 ns/op   (-8%)
BenchmarkResizeLinearLight/Up_Lanczos_PNG_LinearLightPremultipliedAlpha-12    8382391 ns/op   (-5%)
```

The extra cost stays well below the 35% budget in all cases (upscaling is
actually faster, since the per-tap byte->float conversion disappears), at the
price of one extra `float64` scanline buffer per worker goroutine
(`src.w*4*8` or `src.h*4*8` bytes).

## Trade-offs / limitations

- `NearestNeighbor` ignores the options (no convolution is performed).
- The linear-light path allocates an extra per-goroutine float64 scanline
  buffer (see above).
- With negative-lobe filters (Lanczos etc.) linear values can undershoot
  below 0 or above 1 near sharp edges; they are clamped on output, same as
  the default path clamps its results.
- `LinearLight` without `PremultipliedAlpha` is appropriate for opaque
  images; for images with transparency, combine both options to avoid color
  bleed from transparent pixels.
