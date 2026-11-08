package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/herrfrei/openupsd/internal/core"
)

func TestSanityCheckRefusesBlankImages(t *testing.T) {
	if err := sanityCheck(bytes.Repeat([]byte{0xFF}, core.FlashSize)); err == nil {
		t.Error("erased image accepted")
	}
	if err := sanityCheck(make([]byte, core.FlashSize)); err == nil {
		t.Error("all-zero image accepted")
	}
	if err := sanityCheck(make([]byte, 10)); err == nil {
		t.Error("short image accepted")
	}
	img := make([]byte, core.FlashSize)
	img[5] = 1
	if err := sanityCheck(img); err != nil {
		t.Errorf("plausible image refused: %v", err)
	}
}

func TestTextDumpListsEveryParameter(t *testing.T) {
	img := make([]byte, core.FlashSize)
	img[70] = 6 // CELLS
	out := textDump(img)
	if got := strings.Count(out, "\n"); got != len(core.OpenUPSParams) {
		t.Fatalf("%d lines for %d parameters", got, len(core.OpenUPSParams))
	}
	if !strings.Contains(out, "CELLS=6\n") {
		t.Fatalf("CELLS missing or wrong:\n%s", out)
	}
}
