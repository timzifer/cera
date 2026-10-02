# 0003. Tint transforms, Lab, ICC and CMYK

- Status: accepted
- Date: 2026-10-01
- Milestone: M7

## Context

`colorSpace.rgb` (`color.go`) approximates:

- **Separation and DeviceN** as grey from the largest tint; the tint
  transform is not evaluated. Spot colours in print files and plans
  (corporate red, a coloured layer of a site plan) come out grey.
- **Lab** as grey from L\*.
- **ICCBased** by the number of components only (`/N` as Gray, RGB or CMYK).
- **DeviceCMYK** naively (`(1−c)(1−k)` …), which over-saturates and is
  visibly different from every other viewer.

The spec of the project asks for "simplified ICC". cera must stay pure Go and
cheap for images, where colour conversion runs per pixel.

## Decision

**Separation and DeviceN.**

- The tint transform function is evaluated into the alternate space, which
  is then converted to sRGB as usual.
- Colourant `/None` paints nothing (the object is dropped while recording);
  `/All` is treated like a Separation into the alternate space (black on
  screen).
- Path fills convert once per `scn`; the result is memoised on the graphics
  state, so repeated fills with the same tint cost nothing.
- **Images**: one-component Separation images (8 bits or fewer) become a
  256-entry palette, the plane format M5 already has for indexed images.
  DeviceN images with n ≤ 4 use a LUT sampled on a 17ⁿ grid (17⁴ = 83 521
  entries, 334 KB, built once per colour space and cached), interpolated
  multilinearly; n > 4 evaluate per distinct pixel value with a small
  open-addressing memo per image decode.
- **Shadings** (ADR 0001) sample through the same conversion into their LUT.
- DeviceN `/Attributes` (`/Process`, `/Colorants`, `NChannel`) are read only
  to find the alternate space; no spot-colour simulation beyond the tint
  transform.

**Lab.** Exact CIE L\*a\*b\* → XYZ with the space's `/WhitePoint`, Bradford
adaptation to D65, → linear sRGB → sRGB transfer, clamped. `/Range` clamps
a\* and b\* first.

**ICCBased.** Read the profile with a small parser of our own (header, tag
table, `rXYZ`/`gXYZ`/`bXYZ`, `rTRC`/`gTRC`/`bTRC`, `kTRC`, `wtpt`):

- **Matrix/TRC profiles** (gray and RGB, which is nearly all of them):
  converted exactly — TRC to linear, matrix to XYZ (D50), Bradford to D65,
  sRGB. Profiles equal to sRGB (by their header ID or by their primaries
  within 1e-4) take the identity path.
- **LUT-based profiles** (`A2B0`, almost always CMYK) are not evaluated; the
  space is treated as its `/Alternate`, or by `/N`. Counted as
  `icc-lut` in `Stats.Unsupported` so the corpus shows whether a real CMS
  is ever worth it.
- Parsed profiles are cached per document by object reference.

**DeviceCMYK** (and CMYK ICC falling back to it) uses a fixed polynomial
approximation of a SWOP-like conversion, the one pdf.js uses, instead of the
naive formula. It is a few multiplications per pixel and needs no profile.
JPEG images in CMYK go through the same conversion.

**Rendering intent** (`ri`, `/Intent`) stays ignored; it only matters with a
real CMS. **Output intents** are ignored.

## Consequences

- The approximation keys for Separation/DeviceN, Lab and ICC disappear;
  `icc-lut` remains as an intended approximation.
- Images in Separation/DeviceN keep their compact planes; no image grows to
  four bytes a pixel because of its colour space.
- CMYK output changes for every CMYK file: reference images in tests that
  pinned the naive conversion must be regenerated once.
- No overprint simulation; see ADR 0007.

## Implementation notes

Implemented in `color.go` and `colormath.go`:

- Separation and DeviceN go through their tint transforms; a one-ink
  transform is tabulated at 256 tints. `/None` paints nothing. A tint
  space whose transform does not read is counted as `tint-transform` and
  drawn as grey.
- Lab, CalGray, CalRGB and matrix/TRC ICC profiles convert through CIE
  XYZ, Bradford-adapted to D50. Profiles close to sRGB take the device
  path.
- Spaces named by reference are resolved once per document.

Not done yet:

- **DeviceCMYK** still uses the naive formula; the polynomial
  approximation decided above is open.
- LUT-based ICC profiles fall back on the device space of their component
  count, but are not counted as `icc-lut`.

## Alternatives considered

- **A full CMS** (porting lcms2 or using a Go ICC library): exact for LUT
  profiles, but large, slow per pixel without heavy caching, and a
  dependency for a case the corpus will first have to show matters.
- **Evaluating tint transforms per pixel for images**: correct but slow
  for type 4 functions; the 17ⁿ LUT is within one 8-bit level for every
  smooth transform.
