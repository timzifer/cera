// Ported from github.com/go-pdfkit/reader v0.6.0 (BSD-3-Clause,
// Copyright (c) 2026 the go-pdfkit/reader authors); see LICENSE-go-pdfkit.
// Changed: the row size is bounded by MaxPredictorRow, and TIFF
// differencing works at 1, 2 and 4 bits per component too.

package pdf

import "fmt"

// MaxPredictorRow bounds the bytes of one predictor row. /Columns and
// /Colors are numbers in the file; their product must not size an
// allocation unchecked.
const MaxPredictorRow = 16 << 20

// applyPredictor undoes the PNG or TIFF predictor a Flate or LZW stream was
// filtered through. /Predictor 1, and an absent parameter dictionary, mean
// there is nothing to undo.
func applyPredictor(data []byte, parm Dict, resolve func(Object) Object) ([]byte, error) {
	pred := intParm(parm, "Predictor", 1, resolve)
	if pred <= 1 {
		return data, nil
	}
	colors := intParm(parm, "Colors", 1, resolve)
	bpc := intParm(parm, "BitsPerComponent", 8, resolve)
	columns := intParm(parm, "Columns", 1, resolve)
	if colors < 1 || bpc < 1 || columns < 1 {
		return nil, fmt.Errorf("pdf: predictor: /Colors %d /BitsPerComponent %d /Columns %d", colors, bpc, columns)
	}
	if bits := int64(colors) * int64(bpc) * int64(columns); colors > 1<<16 || bpc > 64 || columns > 1<<30 || bits > 8*MaxPredictorRow {
		return nil, fmt.Errorf("pdf: predictor: rows of %d×%d×%d bits are too long", colors, bpc, columns)
	}
	switch {
	case pred == 2:
		return tiffPredictor(data, colors, bpc, columns)
	case pred >= 10:
		return pngPredictor(data, colors, bpc, columns)
	}
	return nil, fmt.Errorf("pdf: predictor: /Predictor %d is not defined", pred)
}

// rowGeometry gives the bytes per row and the byte distance between a
// sample and the one to its left, both rounded up.
func rowGeometry(colors, bpc, columns int) (rowLen, bpp int) {
	return (colors*bpc*columns + 7) / 8, (colors*bpc + 7) / 8
}

// pngPredictor undoes the per-row filters PNG defines, each row of the
// stream being preceded by its filter type byte.
func pngPredictor(data []byte, colors, bpc, columns int) ([]byte, error) {
	rowLen, bpp := rowGeometry(colors, bpc, columns)
	rows := (len(data) + rowLen) / (rowLen + 1)
	out := make([]byte, 0, rows*rowLen)
	zero := make([]byte, rowLen)
	prev := zero
	for i := 0; i < len(data); {
		ft := data[i]
		i++
		start := len(out)
		out = append(out, data[i:min(i+rowLen, len(data))]...)
		i += len(out) - start
		// A damaged file can end mid-row; the missing bytes are zero.
		for len(out)-start < rowLen {
			out = append(out, 0)
		}
		cur := out[start:]
		switch ft {
		case 0:
		case 1:
			for k := bpp; k < rowLen; k++ {
				cur[k] += cur[k-bpp]
			}
		case 2:
			for k := range rowLen {
				cur[k] += prev[k]
			}
		case 3:
			for k := range rowLen {
				left := 0
				if k >= bpp {
					left = int(cur[k-bpp])
				}
				cur[k] += byte((left + int(prev[k])) / 2)
			}
		case 4:
			for k := range rowLen {
				var left, upLeft byte
				if k >= bpp {
					left, upLeft = cur[k-bpp], prev[k-bpp]
				}
				cur[k] += paeth(left, prev[k], upLeft)
			}
		default:
			return nil, fmt.Errorf("pdf: predictor: unknown PNG row filter %d", ft)
		}
		prev = cur
	}
	return out, nil
}

// paeth is the PNG predictor of that name: the neighbour closest to a+b-c.
func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := abs(p-int(a)), abs(p-int(b)), abs(p-int(c))
	if pa <= pb && pa <= pc {
		return a
	}
	if pb <= pc {
		return b
	}
	return c
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// tiffPredictor undoes horizontal differencing, component by component.
func tiffPredictor(data []byte, colors, bpc, columns int) ([]byte, error) {
	rowLen, bpp := rowGeometry(colors, bpc, columns)
	out := append([]byte{}, data...)
	for r := 0; r+rowLen <= len(out); r += rowLen {
		row := out[r : r+rowLen]
		switch bpc {
		case 8:
			for k := bpp; k < rowLen; k++ {
				row[k] += row[k-bpp]
			}
		case 16:
			for k := bpp; k+1 < rowLen; k += 2 {
				v := uint16(row[k])<<8 | uint16(row[k+1])
				p := uint16(row[k-bpp])<<8 | uint16(row[k-bpp+1])
				v += p
				row[k], row[k+1] = byte(v>>8), byte(v)
			}
		case 1, 2, 4:
			tiffSubByte(row, colors, bpc, columns)
		default:
			return nil, fmt.Errorf("pdf: predictor: TIFF differencing with /BitsPerComponent %d is not supported", bpc)
		}
	}
	return out, nil
}

// tiffSubByte undoes differencing in a row of samples narrower than a byte.
func tiffSubByte(row []byte, colors, bpc, columns int) {
	mask := byte(1<<bpc - 1)
	get := func(i int) byte {
		bit := i * bpc
		return row[bit>>3] >> (8 - bpc - bit&7) & mask
	}
	set := func(i int, v byte) {
		bit := i * bpc
		sh := 8 - bpc - bit&7
		row[bit>>3] = row[bit>>3]&^(mask<<sh) | (v&mask)<<sh
	}
	for i := colors; i < colors*columns; i++ {
		set(i, get(i)+get(i-colors))
	}
}
