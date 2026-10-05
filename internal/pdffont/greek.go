// Ported from github.com/go-pdfkit/pdffont v0.3.1 (BSD-3-Clause,
// Copyright (c) 2026 the go-pdfkit/render authors); see LICENSE-go-pdfkit.

package pdffont

// greekAndMathRunes are the names a mathematical document gives its glyphs.
// They are not in the Latin encodings, so a page of equations addressed by
// name — which is what every TeX document is — could not be read back without
// them: the words came out and the symbols did not.
//
// The names are Adobe's, from the Symbol font and the glyph list every
// producer follows. They are checked against the documents themselves rather
// than taken on trust: a font that carries both a name for a code and a
// ToUnicode entry for it states the same fact twice, and the corpus has tens
// of thousands of such pairs to compare against.
var greekAndMathRunes = map[string]rune{
	// The Greek alphabet, lower case and upper.
	"alpha": 'α', "beta": 'β', "gamma": 'γ', "delta": 'δ', "epsilon": 'ε',
	"zeta": 'ζ', "eta": 'η', "theta": 'θ', "iota": 'ι', "kappa": 'κ',
	"lambda": 'λ', "mu1": 'μ', "nu": 'ν', "xi": 'ξ', "omicron": 'ο',
	"pi": 'π', "rho": 'ρ', "sigma": 'σ', "sigma1": 'ς', "tau": 'τ',
	"upsilon": 'υ', "phi": 'φ', "chi": 'χ', "psi": 'ψ', "omega": 'ω',
	"theta1": 'ϑ', "phi1": 'ϕ', "epsilon1": 'ϵ', "rho1": 'ϱ', "omega1": 'ϖ',
	"kappa1": 'ϰ', "pi1": 'ϖ',
	// Adobe's list gives Delta and Omega the increment and ohm signs, which
	// Unicode says are the same characters as the Greek letters written
	// here — canonically equivalent, and these are the forms a person
	// searching the text would type.
	"Alpha": 'Α', "Beta": 'Β', "Gamma": 'Γ', "Delta": 'Δ', "Epsilon": 'Ε',
	"Zeta": 'Ζ', "Eta": 'Η', "Theta": 'Θ', "Iota": 'Ι', "Kappa": 'Κ',
	"Lambda": 'Λ', "Mu": 'Μ', "Nu": 'Ν', "Xi": 'Ξ', "Omicron": 'Ο',
	"Pi": 'Π', "Rho": 'Ρ', "Sigma": 'Σ', "Tau": 'Τ', "Upsilon": 'Υ',
	"Phi": 'Φ', "Chi": 'Χ', "Psi": 'Ψ', "Omega": 'Ω', "Upsilon1": 'ϒ',

	// The signs an equation is built from.
	"minus": '−', "plus": '+', "equal": '=', "asteriskmath": '∗',
	"lessequal": '≤', "greaterequal": '≥', "notequal": '≠', "approxequal": '≈',
	"equivalence": '≡', "similar": '∼', "congruent": '≅', "proportional": '∝',
	"infinity": '∞', "partialdiff": '∂', "gradient": '∇', "integral": '∫',
	"summation": '∑', "product": '∏', "radical": '√', "angle": '∠',
	"perpendicular": '⊥', "therefore": '∴', "dotmath": '⋅', "bullet3": '∙',
	"element": '∈', "notelement": '∉', "universal": '∀', "existential": '∃',
	"emptyset": '∅', "intersection": '∩', "union": '∪',
	"propersubset": '⊂', "propersuperset": '⊃',
	"reflexsubset": '⊆', "reflexsuperset": '⊇', "notsubset": '⊄',
	"logicaland": '∧', "logicalor": '∨', "logicalnot1": '¬',
	"circleplus": '⊕', "circlemultiply": '⊗',
	"prime": '′', "second": '″', "minute": '′',

	// Arrows, and the brackets a display equation is built out of.
	"arrowleft": '←', "arrowup": '↑', "arrowright": '→', "arrowdown": '↓',
	"arrowboth": '↔', "arrowupdn": '↕',
	"arrowdblleft": '⇐', "arrowdblup": '⇑', "arrowdblright": '⇒',
	"arrowdbldown": '⇓', "arrowdblboth": '⇔',
	"angleleft": '⟨', "angleright": '⟩',
	"bracketlefttp": '⎡', "bracketleftbt": '⎣', "bracketleftex": '⎢',
	"bracketrighttp": '⎤', "bracketrightbt": '⎦', "bracketrightex": '⎥',
	"parenlefttp": '⎛', "parenleftbt": '⎝', "parenleftex": '⎜',
	"parenrighttp": '⎞', "parenrightbt": '⎠', "parenrightex": '⎟',
	"bracelefttp": '⎧', "braceleftmid": '⎨', "braceleftbt": '⎩',
	"bracerighttp": '⎫', "bracerightmid": '⎬', "bracerightbt": '⎭',
	"braceex": '⎪',

	// The ligatures and marks a book is set with.
	"ff": 'ﬀ', "ffi": 'ﬃ', "ffl": 'ﬄ', "dotlessj": 'ȷ',
	"circumflexbig": 'ˆ', "tildebig": '˜',
	"suchthat": '∍', "carriagereturn": '↵', "aleph": 'ℵ',
	"Ifraktur": 'ℑ', "Rfraktur": 'ℜ', "weierstrass": '℘',
	"circlecopyrt": '©', "trademarksans": '™', "trademarkserif": '™',
	"degree1": '°', "florin1": 'ƒ',
	"lozenge": '◊', "spade": '♠', "club": '♣', "heart": '♥', "diamond": '♦',
}
