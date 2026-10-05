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
  is ever worth it. (Revised with #29: CMYK profiles are evaluated; see
  below.)
- Parsed profiles are cached per document by object reference.

**DeviceCMYK** (and CMYK ICC falling back to it) converts through a SWOP
press profile instead of the naive formula, as every reference does
through a profile of its own. JPEG images in CMYK go through the same
conversion. (Revised with #20: the polynomial first decided here is too
far off; see Alternatives.)

**CMYK profiles** (revised with #29). Which profile a CMYK colour
converts through, first match wins:

1. `OpenOptions.NaiveCMYK`: none, device values, over all of the below.
2. An ICCBased space of four components: its own profile, as PDFium and
   MuPDF convert it. A profile cera cannot read falls through to 3 and is
   counted as `icc-lut`; more than `maxCMYKProfiles` (16) distinct ones
   in a document fall through as `icc-budget`.
3. `OpenOptions.CMYKProfile`: the caller's profile, for DeviceCMYK. An
   unreadable one is an error from `OpenWith`, not a silent fallback.
4. The bundled default (CGATS TR 005, SWOP; `internal/cmyk/swop.bin`).

Every profile converts the same way: relative colorimetric with black
point compensation, as Little CMS does (`transicc -t 1 -b`), tabulated as
linear sRGB at 17⁴ nodes, so fills, shading functions and images keep the
one fast path. The escape hatch is per document (`OpenOptions`), not per
render: the image cache holds converted pixels.

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

- **DeviceCMYK** (#20) goes through colord's profile of CGATS TR 005
  (SWOP, coated #5; CC0, its characterization data from NPES/CGATS, which
  allows derived profiles with the report named as the source).
  `internal/cmyk/gen` extracts its colorimetric table (A2B1: input
  curves, a 9⁴ Lab grid, output curves) and its black point into
  `internal/cmyk/swop.bin` (54 KB); `internal/cmyk` evaluates them as
  Little CMS does with that profile to sRGB, relative colorimetric with
  black point compensation (`transicc -t 1 -b`), within one level. On
  first use it tabulates the result as linear sRGB at 17⁴ nodes, which
  fills, shading functions and images interpolate over the simplex of a
  cell (within 4 levels of Little CMS, mean 0.3); images take ~10–30 ns
  a pixel more to decode than naively (once, they are cached).
  `OpenOptions.NaiveCMYK` keeps the device values.
- Measured (#20): the flat primaries come within about 11 levels of
  MuPDF and Ghostscript (cyan 0 174 240 against their 0 174 239, magenta
  236 11 141 against 236 0 140, black 43 40 41 against 35 31 32): they
  convert through Artifex's SWOP-like profile, which is AGPL. In
  `accuracy/reference` cera's outlier share on `pdfjs/cmykjpeg.pdf`
  falls from 51 % to 0.7 %; against PDFium it falls from 3.97 % to
  0.80 % there and from 88.95 % to 71.59 % on
  `pdfjs/function_based_shading_cmyk.pdf`.
- **CMYK profiles** (#29): `internal/cmyk` reads a CMYK profile's A2B1
  table (A2B0 if it has none) of type lut8, lut16 or lutAtoB (A curves,
  CLUT, M curves, matrix, B curves; the CLUT the same size along each
  ink), with a Lab or XYZ connection space, and finds its black point as
  Little CMS does for the relative colorimetric intent: for an output
  profile Lab black through B2A0 and back through A2B1 (for a v4 profile
  from the v4 perceptual black, as Little CMS compensates on the way
  in), for any other the darkest colorant; L\* at most 50, neutral.
  `gen` uses the same parser. Over the 20 CMYK press profiles Windows
  ships (Adobe's, FOGRA, GRACoL, Japan Color; v2, lut16 A2B, lut8 B2A)
  it comes within 0.3–0.6 levels of Little CMS 2.18 on average; the
  largest differences, up to 14 levels, are colours outside sRGB, where
  Little CMS interpolates between clipped nodes of its optimized
  transform and cera clips after interpolating. The tests carry three
  profiles made from the bundled table (lut16 v2, lutAtoB v4, a toy
  grid of two nodes) and Little CMS's output for each.
- Tables are shared by the hash of the profile while any document holds
  them (weak pointers), ~1 MB each, made on first use; an ICCBased space
  is resolved once per profile object.
- **What the references convert with** (#29): on the borb pages of the
  issue (`0135` p3, `0525` p1, `0590` p1) the differing colours are
  DeviceCMYK fills and a DeviceCMYK JPEG, no embedded profiles. PDFium
  and MuPDF convert DeviceCMYK close to Adobe's U.S. Web Coated (SWOP)
  v2 with black point compensation; with that profile as
  `CMYKProfile`, cera draws 61 112 183 (MuPDF 60 112 183, PDFium
  50 112 183) for the blue of `0135`, 237 23 76 (237 22–23 75) for its
  red, 36 29 12 (34–38 23–28 10–11) for the brown of `0590` and
  14 20 30 (12–13 18–20 27–30) for the dark image of `0525`. The bundled
  TR 005 profile stays the default, since Adobe's may be redistributed
  only unmodified; the remaining difference with it is the difference
  between the two press characterizations. `0561` is not a CMYK page: it
  is an RGB JPEG under a `/DefaultRGB` of Adobe RGB (1998), which cera
  does not apply (through it the magenta becomes 229 1 127, the
  references' 230 1 127).
- LUT-based grey and RGB ICC profiles fall back on the device space of
  their component count, and are not counted as `icc-lut`.

## Alternatives considered

- **A full CMS** (porting lcms2 or using a Go ICC library): exact for LUT
  profiles, but large, slow per pixel without heavy caching, and a
  dependency for a case the corpus will first have to show matters.
- **pdf.js's polynomial for DeviceCMYK** (a fit to US Web Coated SWOP;
  Apache-2.0): small and fast, but measured up to 61 levels off the
  references (yellow 255 235 61 against 255 242 0, magenta 251 49 153
  against 236 0 140).
- **Ghostscript's `default_cmyk.icc`**: matches every reference within a
  level (MuPDF uses the same Artifex profile), but is AGPL, and cera is
  MIT. The generator reads any CMYK lut16 profile with a Lab connection
  space, so a profile under a licence that allows it can replace TR 005
  with one command.
- **Adobe's U.S. Web Coated (SWOP) v2**: may be redistributed only
  unmodified.
- **Evaluating tint transforms per pixel for images**: correct but slow
  for type 4 functions; the 17ⁿ LUT is within one 8-bit level for every
  smooth transform.
