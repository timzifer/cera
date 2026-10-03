// Command gen compiles the predefined CMaps of PDF 2.0 Table 116, and the
// CID-to-Unicode tables of the Adobe CJK character collections, from a
// checkout of https://github.com/adobe-type-tools/cmap-resources into
// internal/cmap/data.bin.
//
//	go run ./internal/cmap/gen -src ../cmap-resources
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/timzifer/cera/internal/cmap"
)

// predefined are the CMaps a PDF may name without embedding them.
var predefined = []string{
	// Chinese (simplified)
	"GB-EUC-H", "GB-EUC-V", "GBpc-EUC-H", "GBpc-EUC-V", "GBK-EUC-H", "GBK-EUC-V",
	"GBKp-EUC-H", "GBKp-EUC-V", "GBK2K-H", "GBK2K-V",
	"UniGB-UCS2-H", "UniGB-UCS2-V", "UniGB-UTF16-H", "UniGB-UTF16-V",
	// Chinese (traditional)
	"B5pc-H", "B5pc-V", "HKscs-B5-H", "HKscs-B5-V", "ETen-B5-H", "ETen-B5-V",
	"ETenms-B5-H", "ETenms-B5-V", "CNS-EUC-H", "CNS-EUC-V",
	"UniCNS-UCS2-H", "UniCNS-UCS2-V", "UniCNS-UTF16-H", "UniCNS-UTF16-V",
	// Japanese
	"83pv-RKSJ-H", "90ms-RKSJ-H", "90ms-RKSJ-V", "90msp-RKSJ-H", "90msp-RKSJ-V",
	"90pv-RKSJ-H", "Add-RKSJ-H", "Add-RKSJ-V", "EUC-H", "EUC-V",
	"Ext-RKSJ-H", "Ext-RKSJ-V", "H", "V",
	"UniJIS-UCS2-H", "UniJIS-UCS2-V", "UniJIS-UCS2-HW-H", "UniJIS-UCS2-HW-V",
	"UniJIS-UTF16-H", "UniJIS-UTF16-V",
	// Korean
	"KSC-EUC-H", "KSC-EUC-V", "KSCms-UHC-H", "KSCms-UHC-V",
	"KSCms-UHC-HW-H", "KSCms-UHC-HW-V", "KSCpc-EUC-H",
	"UniKS-UCS2-H", "UniKS-UCS2-V", "UniKS-UTF16-H", "UniKS-UTF16-V",
}

// unicode names, per collection, the UTF-32 CMap whose inverse is the
// collection's CID-to-Unicode table.
var unicode = map[string]string{
	"Japan1": "UniJIS-UTF32-H",
	"GB1":    "UniGB-UTF32-H",
	"CNS1":   "UniCNS-UTF32-H",
	"Korea1": "UniKS-UTF32-H",
}

func main() {
	src := flag.String("src", "", "checkout of adobe-type-tools/cmap-resources")
	out := flag.String("o", "internal/cmap/data.bin", "output file")
	flag.Parse()
	if *src == "" {
		log.Fatal("gen: -src is required")
	}
	paths := map[string]string{}
	files, _ := filepath.Glob(filepath.Join(*src, "*", "CMap", "*"))
	for _, f := range files {
		paths[filepath.Base(f)] = f
	}
	parsed := map[string]*cmap.CMap{}
	var load func(name string) *cmap.CMap
	load = func(name string) *cmap.CMap {
		if c, ok := parsed[name]; ok {
			return c
		}
		p, ok := paths[name]
		if !ok {
			log.Fatalf("gen: no CMap %s", name)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			log.Fatal(err)
		}
		c, err := cmap.Parse(data, load)
		if err != nil {
			log.Fatalf("gen: %s: %v", name, err)
		}
		if c.Name != name {
			log.Fatalf("gen: %s calls itself %s", name, c.Name)
		}
		parsed[name] = c
		return c
	}
	entries := map[string][]byte{}
	var add func(c *cmap.CMap)
	add = func(c *cmap.CMap) {
		if _, ok := entries[c.Name]; ok {
			return
		}
		entries[c.Name] = cmap.Marshal(c)
		if p := c.Parent(); p != nil {
			add(p)
		}
	}
	for _, name := range predefined {
		add(load(name))
	}
	for coll, name := range unicode {
		m := map[uint32]rune{}
		load(name).Each(func(code uint32, n int, cid int) {
			r := rune(code)
			if old, ok := m[uint32(cid)]; n == 4 && (!ok || better(r, old)) {
				m[uint32(cid)] = r
			}
		})
		// The vertical CMap selects vertical forms by their horizontal
		// characters, which is what those forms read as.
		load(name[:len(name)-1] + "V").Each(func(code uint32, n int, cid int) {
			r := rune(code)
			if old, ok := m[uint32(cid)]; n == 4 && (!ok || better(r, old)) {
				m[uint32(cid)] = r
			}
		})
		entries["ucs:"+coll] = cmap.MarshalUnicode(m)
	}
	data, err := cmap.Bundle(entries)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s: %d entries, %d bytes\n", *out, len(entries), len(data))
}

// better reports whether r is a better reading of a CID than old: unified
// ideographs and ordinary characters before compatibility forms, radicals
// and private use, then the lower code point.
func better(r, old rune) bool {
	if a, b := rank(r), rank(old); a != b {
		return a < b
	}
	return r < old
}

func rank(r rune) int {
	switch {
	case r >= 0xE000 && r <= 0xF8FF, r >= 0xF0000:
		return 3
	case r >= 0xF900 && r <= 0xFAFF, r >= 0x2F800 && r <= 0x2FA1F,
		r >= 0x2E80 && r <= 0x2FDF, r >= 0xFE30 && r <= 0xFE4F, r >= 0xFE10 && r <= 0xFE1F:
		// Compatibility ideographs, radicals and vertical forms: other
		// codes say the same thing more plainly.
		return 2
	case r >= 0xFF00 && r <= 0xFFEF:
		return 1
	}
	return 0
}
