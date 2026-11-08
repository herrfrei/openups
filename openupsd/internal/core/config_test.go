package core

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

type goldenFile struct {
	Decode []struct {
		Name string `json:"name"`
		Raw  string `json:"raw"`
		Want string `json:"want"`
	} `json:"decode"`
}

func loadGolden(t *testing.T) goldenFile {
	t.Helper()
	b, err := os.ReadFile("testdata/openups_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g goldenFile
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func mustParam(t *testing.T, name string) Param {
	t.Helper()
	p, ok := ParamByName(name)
	if !ok {
		t.Fatalf("unknown parameter %s", name)
	}
	return p
}

// Decoding must match pyopenups for every recorded raw pattern.
func TestDecodeMatchesPython(t *testing.T) {
	g := loadGolden(t)
	if len(g.Decode) < 500 {
		t.Fatalf("golden file looks truncated: %d decode cases", len(g.Decode))
	}
	for _, c := range g.Decode {
		p := mustParam(t, c.Name)
		raw, err := hex.DecodeString(c.Raw)
		if err != nil {
			t.Fatal(err)
		}
		img := make([]byte, 1024)
		copy(img[p.Index:], raw)
		got, err := p.Decode(img)
		if err != nil {
			t.Fatalf("%s %s: %v", c.Name, c.Raw, err)
		}
		if got != c.Want {
			t.Errorf("%s raw=%s: got %q, python %q", c.Name, c.Raw, got, c.Want)
		}
	}
}

func TestParamTableInvariants(t *testing.T) {
	if len(OpenUPSParams) != 49 {
		t.Fatalf("expected 49 parameters, have %d", len(OpenUPSParams))
	}
	owner := make([]string, 1024)
	names := map[string]bool{}
	for _, p := range OpenUPSParams {
		if names[p.Name] {
			t.Errorf("duplicate parameter %s", p.Name)
		}
		names[p.Name] = true
		if p.Index < 0 || p.Index+p.Len > 1024 {
			t.Errorf("%s outside the settings area", p.Name)
			continue
		}
		for i := p.Index; i < p.Index+p.Len; i++ {
			if owner[i] != "" {
				t.Errorf("%s overlaps %s at byte %d", p.Name, owner[i], i)
			}
			owner[i] = p.Name
		}
		// A parameter that straddles a 16 byte page would need two page writes.
		if p.Index/16 != (p.Index+p.Len-1)/16 {
			t.Errorf("%s straddles a flash page", p.Name)
		}
		// every parameter must decode its zero image without error
		if _, err := p.Decode(make([]byte, 1024)); err != nil {
			t.Errorf("%s: decode of zero image: %v", p.Name, err)
		}
	}
}

// A real flash image read from an OpenUPS (firmware 1.9, LiFePO4, 6 cells) and
// the values the decoder must produce for it. Unlike the golden vectors this
// is not derived from the Python code: it pins offsets and scales to hardware.
func TestRealDeviceImage(t *testing.T) {
	img, err := os.ReadFile("testdata/openups_real_flash.bin")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/openups_real_cfg.txt")
	if err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	for _, p := range OpenUPSParams {
		v, err := p.Decode(img)
		if err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		fmt.Fprintf(&got, "%s=%s\n", p.Name, v)
	}
	if strings.ReplaceAll(got.String(), "\r\n", "\n") != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Fatalf("decoded image differs from the dump taken from the device:\n%s", got.String())
	}
}
