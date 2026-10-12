package cera

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/timzifer/cera/internal/testpdf"
)

// viewSettingsFile is a document of three pages whose catalogue has extra.
func viewSettingsFile(extra string) []byte {
	return testpdf.File{
		Objs: map[int]string{
			1: "<</Type /Catalog /Pages 2 0 R /Dests <</start [4 0 R /FitH 700]>> " + extra + ">>",
			2: "<</Type /Pages /Kids [3 0 R 4 0 R 5 0 R] /Count 3 /MediaBox [0 0 600 800]>>",
			3: "<</Type /Page /Parent 2 0 R>>",
			4: "<</Type /Page /Parent 2 0 R>>",
			5: "<</Type /Page /Parent 2 0 R>>",
			9: "<</Direction /R2L /DisplayDocTitle true /NumCopies 3>>",
		},
		Trailer: "/Root 1 0 R",
	}.Bytes()
}

func TestViewSettings(t *testing.T) {
	defaults := ViewSettings{PageMode: "UseNone", AfterFullScreen: "UseNone", PageLayout: "SinglePage",
		Print: PrintSettings{Scaling: "AppDefault", Copies: 1}}
	with := func(f func(*ViewSettings)) ViewSettings {
		v := defaults
		f(&v)
		return v
	}
	for _, c := range []struct {
		catalog string
		want    ViewSettings
	}{
		{"", defaults},
		{"/PageMode /UseOutlines /PageLayout /TwoPageRight", with(func(v *ViewSettings) {
			v.PageMode, v.PageLayout = "UseOutlines", "TwoPageRight"
		})},
		// What the specification does not know, and what is of another type.
		{"/PageMode /Sideways /PageLayout (TwoColumnLeft) /ViewerPreferences [1 2]", defaults},
		{"/PageMode /FullScreen /ViewerPreferences <</NonFullScreenPageMode /UseThumbs /FitWindow true /CenterWindow true" +
			" /HideToolbar true /HideMenubar true /HideWindowUI true /Direction /L2R /DisplayDocTitle (yes)>>", with(func(v *ViewSettings) {
			v.PageMode, v.AfterFullScreen = "FullScreen", "UseThumbs"
			v.FitWindow, v.CenterWindow = true, true
			v.HideToolbar, v.HideMenubar, v.HideWindowUI = true, true, true
		})},
		{"/ViewerPreferences 9 0 R", with(func(v *ViewSettings) {
			v.RightToLeft, v.DisplayDocTitle, v.Print.Copies = true, true, 3
		})},
		// Full screen is no mode to return to, and UseAttachments none either.
		{"/ViewerPreferences <</NonFullScreenPageMode /FullScreen>>", defaults},
		{"/ViewerPreferences <</PrintScaling /None /Duplex /DuplexFlipLongEdge /PickTrayByPDFSize true /NumCopies 0" +
			" /PrintPageRange [1 1 2 9 3 2 0 1 7 8 3 (x) 3]>>", with(func(v *ViewSettings) {
			v.Print = PrintSettings{Scaling: "None", Duplex: "DuplexFlipLongEdge", PickTrayByPDFSize: true, Copies: 1,
				Ranges: [][2]int{{0, 0}, {1, 2}}}
		})},
		{"/ViewerPreferences <</PrintScaling /Shrink /Duplex /Both /NumCopies -2 /PrintPageRange 4>>", defaults},
	} {
		doc := editOpen(t, viewSettingsFile(c.catalog))
		if got := doc.ViewSettings(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.catalog, got, c.want)
		}
	}
}

func TestViewSettingsOpen(t *testing.T) {
	for _, c := range []struct {
		open, want string
	}{
		{"[5 0 R /XYZ 0 800 2]", "page 2 XYZ"},
		{"[4 0 R /Fit]", "page 1 Fit"},
		{"/start", "page 1 FitH"},
		{"(start)", "page 1 FitH"},
		{"<</S /GoTo /D [5 0 R /FitH 300]>>", "page 2 FitH"},
		{"<</S /GoTo /D /start>>", "page 1 FitH"},
		{"<</S /Named /N /LastPage>>", "LastPage"},
		// A script first, then the place: the first action that leads somewhere.
		{"<</S /JavaScript /JS (app.alert(1)) /Next <</S /GoTo /D [4 0 R /Fit]>>>>", "page 1 Fit"},
		// Nothing a viewer does because a file was opened.
		{"<</S /JavaScript /JS (app.alert(1))>>", ""},
		{"<</S /URI /URI (https://example.com/)>>", ""},
		{"<</S /Launch /F (calc.exe)>>", ""},
		{"<</S /GoToR /F (other.pdf) /D [0 /Fit]>>", ""},
		{"<</S /Named /N /Print>>", ""},
		// No destination.
		{"42", ""},
		{"[]", ""},
		{"[99 0 R /Fit]", ""},
		{"/nowhere", "named nowhere"},
	} {
		doc := editOpen(t, viewSettingsFile("/OpenAction "+c.open))
		got := ""
		switch o := doc.ViewSettings().Open; {
		case o == nil:
		case o.NamedAction != "":
			got = o.NamedAction
		case o.Page < 0:
			got = "named " + o.Named
		default:
			got = fmt.Sprintf("page %d %s", o.Page, o.Fit)
		}
		if got != c.want {
			t.Errorf("/OpenAction %s: %q, want %q", c.open, got, c.want)
		}
		// What is returned is what Document.Destination takes.
		if o := doc.ViewSettings().Open; o != nil && o.Page >= 0 {
			if d, ok := doc.Destination(o); !ok || d.Page != o.Page {
				t.Errorf("/OpenAction %s: destination %+v, %v", c.open, d, ok)
			}
		}
	}
}
