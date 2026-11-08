package core

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func realProfile(t *testing.T) BatteryProfile {
	t.Helper()
	img, err := os.ReadFile("testdata/openups_real_flash.bin")
	if err != nil {
		t.Fatal(err)
	}
	p, err := ProfileFromFlash(img)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var t0 = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func approx(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.4f, want %.4f (±%.4f)", name, got, want, tol)
	}
}

// cells returns six equal cell voltages.
func cells(v float64) []float64 { return []float64{v, v, v, v, v, v} }

func TestProfileFromRealDevice(t *testing.T) {
	p := realProfile(t)
	approx(t, "capacity Ah", p.CapacityAh, 20, 1e-9)
	if p.Cells != 6 {
		t.Errorf("cells = %d", p.Cells)
	}
	approx(t, "cutoff", p.CutoffCell, 2.80239, 1e-5)
	approx(t, "bulk stop", p.BulkStop, 3.49656, 1e-5)
	if len(p.OCV) != 6 {
		t.Fatalf("ocv points %d", len(p.OCV))
	}
	approx(t, "ocv 0%", p.OCV[0].Volts, 3.10234, 1e-5)
	approx(t, "ocv 100%", p.OCV[5].Volts, 3.35087, 1e-5)
}

func TestProfileRejectsImplausibleFlash(t *testing.T) {
	if _, err := ProfileFromFlash(make([]byte, 10)); err == nil {
		t.Error("short image accepted")
	}
	blank := make([]byte, FlashSize)
	for i := range blank {
		blank[i] = 0xFF
	}
	if _, err := ProfileFromFlash(blank); err == nil {
		t.Error("blank flash accepted")
	}
	if _, err := ProfileFromFlash(make([]byte, FlashSize)); err == nil {
		t.Error("all-zero flash accepted")
	}
	img, _ := os.ReadFile("testdata/openups_real_flash.bin")
	bad := append([]byte(nil), img...)
	cp, _ := ParamByName("CELLS")
	bad[cp.Index] = 9
	if _, err := ProfileFromFlash(bad); err == nil {
		t.Error("9 cells accepted")
	}
	// flatten the OCV table: 50% point equal to the 25% point
	o25, _ := ParamByName("OCV_SOC25")
	o50, _ := ParamByName("OCV_SOC50")
	bad = append([]byte(nil), img...)
	copy(bad[o50.Index:o50.Index+o50.Len], bad[o25.Index:o25.Index+o25.Len])
	if _, err := ProfileFromFlash(bad); err == nil {
		t.Error("non-rising OCV table accepted")
	}
}

func TestSOCFromCell(t *testing.T) {
	p := realProfile(t)
	approx(t, "below 0%", p.SOCFromCell(2.9), 0, 0)
	approx(t, "0%", p.SOCFromCell(3.10234), 0, 1e-9)
	approx(t, "100%", p.SOCFromCell(3.35087), 1, 1e-9)
	approx(t, "above 100%", p.SOCFromCell(3.6), 1, 0)
	approx(t, "50%", p.SOCFromCell(3.29945), 0.5, 1e-9)
	approx(t, "halfway 25..50", p.SOCFromCell((3.24803+3.29945)/2), 0.375, 1e-9)
	approx(t, "roundtrip", p.SOCFromCell(p.SOCtoCell(0.62)), 0.62, 1e-9)
}

// restored builds an estimator that starts at the given charge.
func restored(t *testing.T, soc float64) *Estimator {
	t.Helper()
	p := realProfile(t)
	path := filepath.Join(t.TempDir(), "state.json")
	e := NewEstimator(p, path, t0)
	e.soc, e.initialised, e.tracking = soc, true, true
	if err := e.Save(t0); err != nil {
		t.Fatal(err)
	}
	return NewEstimator(p, path, t0.Add(time.Minute))
}

func TestFirstSampleInitialisesFromVoltage(t *testing.T) {
	p := realProfile(t)
	e := NewEstimator(p, "", t0)
	est := e.Update(Sample{At: t0, VBat: 6 * 3.15, Cells: cells(3.15)})
	approx(t, "charge", est.ChargePct, 100*p.SOCFromCell(3.15), 1e-6)
	if est.Tracking {
		t.Error("a voltage-only guess must not claim to be tracking")
	}
}

func TestCoulombCountingOnDischarge(t *testing.T) {
	e := restored(t, 1.0)
	now := t0.Add(time.Minute)
	var est Estimate
	// 2 A for one hour from a 20 Ah pack = 10 %
	for i := 0; i <= 720; i++ {
		est = e.Update(Sample{At: now.Add(time.Duration(i) * 5 * time.Second), OnBattery: true,
			VBat: 19.5, Cells: cells(3.25), DischargeA: 2, OutputW: 36})
	}
	approx(t, "charge after 1 h", est.ChargePct, 90, 0.2)
	if !est.RuntimeOK {
		t.Fatal("no runtime on battery")
	}
	approx(t, "runtime h", est.Runtime.Hours(), 0.9*20/2, 0.05) // 9 h
	if !est.Tracking {
		t.Error("restored count should be tracking")
	}
}

func TestChargingAddsWithEfficiency(t *testing.T) {
	e := restored(t, 0.5)
	now := t0.Add(time.Minute)
	var est Estimate
	for i := 0; i <= 360; i++ { // 30 minutes at 4 A
		est = e.Update(Sample{At: now.Add(time.Duration(i) * 5 * time.Second), Charging: true,
			VBat: 20, Cells: cells(3.33), ChargeA: 4, OutputW: 30})
	}
	approx(t, "charge", est.ChargePct, 50+100*4*0.5*0.99/20, 0.2)
}

func TestFullAnchorAfterConstantVoltagePhase(t *testing.T) {
	e := restored(t, 0.80) // the count drifted low
	now := t0.Add(time.Minute)
	e.Update(Sample{At: now, Charging: true, VBat: 21, Cells: cells(3.49), ChargeA: 3, OutputW: 30})
	now = now.Add(5 * time.Second)
	est := e.Update(Sample{At: now, Charging: true, VBat: 21, Cells: cells(3.495), ChargeA: 0.9, OutputW: 30})
	if est.ChargePct >= 100 {
		t.Fatal("anchored while still charging")
	}
	now = now.Add(5 * time.Second)
	est = e.Update(Sample{At: now, Charging: false, VBat: 20.8, Cells: cells(3.47), ChargeA: 0.01, OutputW: 30})
	approx(t, "charge after end of CV", est.ChargePct, 100, 1e-9)
	if !est.Tracking {
		t.Error("full anchor should mean tracking")
	}
}

func TestIdleOnMainsWithoutCVDoesNotClaimFull(t *testing.T) {
	e := restored(t, 0.6)
	now := t0.Add(time.Minute)
	var est Estimate
	for i := 0; i < 20; i++ { // charger idle at a mid voltage: could be anywhere on the flat curve
		est = e.Update(Sample{At: now.Add(time.Duration(i) * 5 * time.Second), VBat: 19.8,
			Cells: cells(3.30), OutputW: 30})
	}
	approx(t, "charge", est.ChargePct, 60, 1e-6)
}

func TestEmptyAnchorAtCutoff(t *testing.T) {
	e := restored(t, 0.25)
	est := e.Update(Sample{At: t0.Add(time.Minute), OnBattery: true, VBat: 16.5,
		Cells: []float64{2.85, 2.9, 2.82, 2.9, 2.88, 2.87}, DischargeA: 3, OutputW: 40})
	approx(t, "charge at cutoff", est.ChargePct, 0, 1e-9)
	if !est.RuntimeOK || est.Runtime != 0 {
		t.Errorf("runtime = %v ok=%v", est.Runtime, est.RuntimeOK)
	}
}

func TestRestCorrectionOnlyInSteepLowRegion(t *testing.T) {
	// low region: pack rests at 3.15 V/cell (about 10 % by the curve) while the count says 40 %
	e := restored(t, 0.40)
	now := t0.Add(time.Minute)
	var est Estimate
	for i := 0; i < 360; i++ { // 30 minutes at rest
		est = e.Update(Sample{At: now.Add(time.Duration(i) * 5 * time.Second), VBat: 18.9, Cells: cells(3.15), OutputW: 20})
	}
	want := 100 * realProfile(t).SOCFromCell(3.15)
	approx(t, "pulled to the curve", est.ChargePct, want, 1.5)

	// flat region: the same disagreement must be ignored
	e = restored(t, 0.40)
	for i := 0; i < 360; i++ {
		est = e.Update(Sample{At: now.Add(time.Duration(i) * 5 * time.Second), VBat: 19.8, Cells: cells(3.30), OutputW: 20})
	}
	approx(t, "flat region untouched", est.ChargePct, 40, 1e-6)
}

func TestGapIsNotIntegrated(t *testing.T) {
	e := restored(t, 0.8)
	now := t0.Add(time.Minute)
	e.Update(Sample{At: now, OnBattery: true, VBat: 19.5, Cells: cells(3.25), DischargeA: 5, OutputW: 80})
	est := e.Update(Sample{At: now.Add(30 * time.Minute), OnBattery: true, VBat: 19.5, Cells: cells(3.25), DischargeA: 5, OutputW: 80})
	approx(t, "charge", est.ChargePct, 80, 1e-6)
}

func TestRuntimeOnMainsUsesLoadPowerAndEfficiency(t *testing.T) {
	p := realProfile(t)
	e := restored(t, 1.0)
	var est Estimate
	now := t0.Add(time.Minute)
	for i := 0; i < 30; i++ {
		est = e.Update(Sample{At: now.Add(time.Duration(i) * 5 * time.Second), VBat: 20.9,
			Cells: cells(3.35), Charging: false, OutputW: 36})
	}
	if !est.RuntimeOK {
		t.Fatal("no runtime on mains")
	}
	amps := 36 / (6 * p.nominalCell() * 0.9)
	approx(t, "runtime h", est.Runtime.Hours(), 20/amps, 0.01)
}

func TestEfficiencyIsLearnedSlowly(t *testing.T) {
	e := restored(t, 1.0)
	now := t0.Add(time.Minute)
	for i := 0; i < 1000; i++ { // 36 W out of 20 V * 2.4 A = 75 %
		e.Update(Sample{At: now.Add(time.Duration(i) * 5 * time.Second), OnBattery: true,
			VBat: 20, Cells: cells(3.33), DischargeA: 2.4, OutputW: 36})
	}
	approx(t, "eta", e.Efficiency(), 0.75, 0.01)
	// a single odd sample must not swing it
	e2 := restored(t, 1.0)
	e2.Update(Sample{At: now, OnBattery: true, VBat: 20, Cells: cells(3.33), DischargeA: 2.4, OutputW: 36})
	approx(t, "one sample", e2.Efficiency(), 0.9, 0.005)
}

func TestNoRuntimeWithoutLoad(t *testing.T) {
	e := restored(t, 1.0)
	est := e.Update(Sample{At: t0.Add(time.Minute), OnBattery: true, VBat: 20, Cells: cells(3.33), DischargeA: 0.01, OutputW: 0.1})
	if est.RuntimeOK {
		t.Errorf("runtime %v for a 10 mA load", est.Runtime)
	}
	est = e.Update(Sample{At: t0.Add(2 * time.Minute), VBat: 20, Cells: cells(3.33), OutputW: 0.2})
	if est.RuntimeOK {
		t.Error("runtime for an idle output on mains")
	}
}

func TestRuntimeIsCapped(t *testing.T) {
	e := restored(t, 1.0)
	est := e.Update(Sample{At: t0.Add(time.Minute), OnBattery: true, VBat: 20, Cells: cells(3.33), DischargeA: 0.06, OutputW: 1.1})
	if !est.RuntimeOK || est.Runtime != maxRuntime {
		t.Errorf("runtime %v, want cap %v", est.Runtime, maxRuntime)
	}
}

func TestStatePersistence(t *testing.T) {
	p := realProfile(t)
	path := filepath.Join(t.TempDir(), "sub", "state.json")

	e := NewEstimator(p, path, t0)
	e.Update(Sample{At: t0, VBat: 19.8, Cells: cells(3.30)})
	e.soc = 0.77
	if err := e.Save(t0); err != nil {
		t.Fatal(err)
	}

	// recent, same battery: restored and tracking
	r := NewEstimator(p, path, t0.Add(10*time.Minute))
	est := r.Update(Sample{At: t0.Add(10 * time.Minute), VBat: 19.8, Cells: cells(3.30)})
	approx(t, "restored charge", est.ChargePct, 77, 1e-6)
	if !est.Tracking {
		t.Error("restored state should be tracking")
	}

	// too old: back to the voltage
	old := NewEstimator(p, path, t0.Add(3*time.Hour))
	est = old.Update(Sample{At: t0.Add(3 * time.Hour), VBat: 19.8, Cells: cells(3.30)})
	if est.Tracking {
		t.Error("stale state was trusted")
	}

	// battery reconfigured: ignored
	q := p
	q.CapacityAh = 7
	if NewEstimator(q, path, t0.Add(time.Minute)).initialised {
		t.Error("state for another capacity was trusted")
	}

	// corrupt file and missing file: no crash, no state
	if err := os.WriteFile(path, []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if NewEstimator(p, path, t0).initialised {
		t.Error("corrupt state accepted")
	}
	if NewEstimator(p, filepath.Join(t.TempDir(), "missing.json"), t0).initialised {
		t.Error("missing state accepted")
	}
}

func TestSaveIfDueRateLimits(t *testing.T) {
	p := realProfile(t)
	path := filepath.Join(t.TempDir(), "state.json")
	e := NewEstimator(p, path, t0)
	e.Update(Sample{At: t0, VBat: 19.8, Cells: cells(3.30)})
	if err := e.SaveIfDue(t0); err != nil {
		t.Fatal(err)
	}
	st1, _ := os.Stat(path)
	e.soc -= 0.1
	if err := e.SaveIfDue(t0.Add(10 * time.Second)); err != nil { // too soon
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if st2, _ := os.Stat(path); !st2.ModTime().Equal(st1.ModTime()) || len(b) == 0 {
		t.Error("saved again within a minute")
	}
	if err := e.SaveIfDue(t0.Add(2 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	r := NewEstimator(p, path, t0.Add(3*time.Minute))
	approx(t, "saved charge", r.soc, e.soc, 1e-9)
}

func TestApplyAddsEstimateToReading(t *testing.T) {
	e := restored(t, 0.5)
	r := Reading{Status: "OB DISCHRG"}
	r.SetNum("vbat", 19.6)
	for i := 1; i <= 6; i++ {
		r.SetNum("vcell_"+string(rune('0'+i)), 3.27)
	}
	r.SetNum("c_discharge", 2)
	r.SetNum("output_power", 36)
	e.Apply(&r, t0.Add(time.Minute))
	if r.Num["battery_charge"] != 50 {
		t.Errorf("battery_charge = %v", r.Num["battery_charge"])
	}
	if got, want := r.Num["runtime"], 5.0*3600; math.Abs(got-want) > 1 {
		t.Errorf("runtime = %v, want about %v", got, want)
	}
	if r.Str["estimate_state"] != "tracking" {
		t.Errorf("state = %q", r.Str["estimate_state"])
	}
	dev := OpenUPS.WithSensors(EstimateSensors()...)
	vars := dev.NUTVars(r, "S")
	if vars["battery.charge"] != "50" || vars["battery.runtime"] == "" {
		t.Errorf("NUT vars: charge=%q runtime=%q", vars["battery.charge"], vars["battery.runtime"])
	}
	if len(OpenUPS.Sensors) == len(dev.Sensors) {
		t.Error("WithSensors modified or did not extend the sensor list")
	}

	// a reading without a battery voltage is left untouched
	var empty Reading
	e.Apply(&empty, t0.Add(2*time.Minute))
	if len(empty.Num) != 0 {
		t.Error("estimate added to an unusable reading")
	}
}
