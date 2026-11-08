package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// This file estimates battery charge and remaining runtime for the OpenUPS.
// The device reports neither (it only offers them in a reply that breaks
// communication), so both are derived from what it does report plus the
// battery description stored in its flash.

// The numbers are estimates, and they are labelled as such: the cell voltage
// of a LiFePO4 pack is almost flat between 25 % and 75 %, so voltage alone
// cannot tell the charge. The estimator therefore counts charge in and out
// (coulomb counting) and corrects the count only at moments where the voltage
// is trustworthy:

//   - end of a constant-voltage charge: the pack is full,
//   - cell voltage at the UPS cutoff while on battery: the pack is empty,
//   - the pack at rest in the steep low end of the OCV curve.

// OCVPoint is one point of the open-circuit-voltage curve, per cell.
type OCVPoint struct {
	Volts float64 // per cell
	SOC   float64 // 0..1
}

// BatteryProfile describes the battery as configured in the UPS flash.
type BatteryProfile struct {
	CapacityAh float64
	Cells      int
	OCV        []OCVPoint // ascending in both fields
	CutoffCell float64    // UPS_VCELL_MIN_STOP: the UPS shuts down here, per cell
	BulkStop   float64    // CHG_BULK_STOP_VOLTAGE: end of the CV phase, per cell
}

func paramFloat(img []byte, name string) (float64, error) {
	p, ok := ParamByName(name)
	if !ok {
		return 0, fmt.Errorf("unknown parameter %s", name)
	}
	s, err := p.Decode(img)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a number", name, s)
	}
	return v, nil
}

// ProfileFromFlash builds the battery profile from a settings image and
// refuses values that cannot be right, so a misread never turns into a
// confident wrong percentage.
func ProfileFromFlash(img []byte) (BatteryProfile, error) {
	var p BatteryProfile
	if len(img) != FlashSize {
		return p, fmt.Errorf("flash image is %d bytes, expected %d", len(img), FlashSize)
	}
	read := func(dst *float64, name string, scale float64) error {
		v, err := paramFloat(img, name)
		if err != nil {
			return err
		}
		*dst = v * scale
		return nil
	}
	var cells float64
	for _, f := range []struct {
		dst   *float64
		name  string
		scale float64
	}{
		{&p.CapacityAh, "CAPACITY", 0.001},
		{&cells, "CELLS", 1},
		{&p.CutoffCell, "UPS_VCELL_MIN_STOP", 1},
		{&p.BulkStop, "CHG_BULK_STOP_VOLTAGE", 1},
	} {
		if err := read(f.dst, f.name, f.scale); err != nil {
			return p, err
		}
	}
	p.Cells = int(cells)
	for _, pt := range []struct {
		name string
		soc  float64
	}{{"OCV_SOC0", 0}, {"OCV_SOC10", .10}, {"OCV_SOC25", .25}, {"OCV_SOC50", .50}, {"OCV_SOC75", .75}, {"OCV_SOC100", 1}} {
		var v float64
		if err := read(&v, pt.name, 1); err != nil {
			return p, err
		}
		p.OCV = append(p.OCV, OCVPoint{Volts: v, SOC: pt.soc})
	}
	switch {
	case p.CapacityAh < 0.1 || p.CapacityAh > 1000:
		return p, fmt.Errorf("battery capacity %.2f Ah is not plausible", p.CapacityAh)
	case p.Cells < 1 || p.Cells > 6:
		return p, fmt.Errorf("cell count %d is not plausible", p.Cells)
	case p.CutoffCell < 1 || p.CutoffCell > 4.5:
		return p, fmt.Errorf("cutoff %.2f V per cell is not plausible", p.CutoffCell)
	case p.BulkStop < 1 || p.BulkStop > 4.5:
		return p, fmt.Errorf("bulk stop %.2f V per cell is not plausible", p.BulkStop)
	}
	for i, pt := range p.OCV {
		if pt.Volts < 1 || pt.Volts > 4.5 || (i > 0 && pt.Volts <= p.OCV[i-1].Volts) {
			return p, errors.New("OCV table is not a rising curve of plausible voltages")
		}
	}
	return p, nil
}

// SOCFromCell maps a resting per-cell voltage to a charge fraction.
func (p BatteryProfile) SOCFromCell(v float64) float64 {
	o := p.OCV
	switch {
	case v <= o[0].Volts:
		return 0
	case v >= o[len(o)-1].Volts:
		return 1
	}
	for i := 1; i < len(o); i++ {
		if v <= o[i].Volts {
			f := (v - o[i-1].Volts) / (o[i].Volts - o[i-1].Volts)
			return o[i-1].SOC + f*(o[i].SOC-o[i-1].SOC)
		}
	}
	return 1
}

// nominalCell is the mid-curve cell voltage, used to turn load power into a
// battery current while on mains, when the battery voltage is not at rest.
func (p BatteryProfile) nominalCell() float64 { return p.SOCtoCell(0.5) }

// SOCtoCell is the inverse of SOCFromCell.
func (p BatteryProfile) SOCtoCell(soc float64) float64 {
	o := p.OCV
	for i := 1; i < len(o); i++ {
		if soc <= o[i].SOC {
			f := (soc - o[i-1].SOC) / (o[i].SOC - o[i-1].SOC)
			return o[i-1].Volts + f*(o[i].Volts-o[i-1].Volts)
		}
	}
	return o[len(o)-1].Volts
}

// Sample is one poll of the UPS, reduced to what the estimator needs.
type Sample struct {
	At         time.Time
	OnBattery  bool
	Charging   bool      // the charger is delivering current
	VBat       float64   // pack voltage
	Cells      []float64 // per-cell voltage, may be empty
	ChargeA    float64   // battery charge current
	DischargeA float64   // battery discharge current
	OutputW    float64
}

// Estimate is the result of one update.
type Estimate struct {
	ChargePct float64
	Runtime   time.Duration
	RuntimeOK bool
	// Tracking is false until the count has been anchored by a real event
	// (full charge, empty, a low-voltage rest) or restored from a recent
	// state file. Until then the charge comes from the voltage alone and can
	// be far off on a flat part of the curve.
	Tracking bool
}

// Tunables. They are conservative defaults, not measurements.
const (
	chargeEff   = 0.99             // coulombic efficiency of LiFePO4
	etaDefault  = 0.90             // output power / battery power, until learned
	gapMax      = 5 * time.Minute  // longer silence: do not integrate across it
	restCurrent = 0.15             // A: below this the pack counts as at rest
	restMin     = 2 * time.Minute  // at rest this long before trusting the voltage
	ocvMaxSOC   = 0.30             // only correct from the OCV curve below this
	cvMargin    = 0.03             // V per cell below BulkStop that still counts as CV
	smoothTau   = 60 * time.Second // smoothing of current and power for the runtime
	maxRuntime  = 48 * time.Hour   // never promise more than this
	stateMaxAge = time.Hour        // a saved state older than this is not trusted
	saveEvery   = time.Minute
	stateFormat = 1
	minCurrent  = 0.05 // A: below this no runtime is given
	minPowerW   = 1.0  // W
	etaAlpha    = 0.01 // per-sample weight when learning efficiency
)

// Estimator tracks charge over time. It is not safe for concurrent use; the
// daemon calls it from the poll loop only.
type Estimator struct {
	p BatteryProfile

	soc         float64
	initialised bool
	tracking    bool

	last     time.Time
	haveLast bool

	sawCV     bool
	restSince time.Time
	resting   bool

	avgI, avgP float64
	haveAvgI   bool
	haveAvgP   bool
	eta        float64

	statePath string
	lastSave  time.Time
	savedSOC  float64
}

type savedState struct {
	Version    int       `json:"version"`
	SOC        float64   `json:"soc"`
	Eta        float64   `json:"eta"`
	SavedAt    time.Time `json:"saved_at"`
	CapacityAh float64   `json:"capacity_ah"`
	Cells      int       `json:"cells"`
}

// NewEstimator creates an estimator. statePath may be empty (no persistence).
// A saved state is used only if it is recent and was made for the same battery
// configuration; otherwise the first sample initialises the charge from the
// voltage.
func NewEstimator(p BatteryProfile, statePath string, now time.Time) *Estimator {
	e := &Estimator{p: p, eta: etaDefault, statePath: statePath, savedSOC: -1}
	if statePath == "" {
		return e
	}
	b, err := os.ReadFile(statePath)
	if err != nil {
		return e
	}
	var s savedState
	if json.Unmarshal(b, &s) != nil || s.Version != stateFormat {
		return e
	}
	if s.Cells != p.Cells || math.Abs(s.CapacityAh-p.CapacityAh) > 1e-6 {
		return e // the battery was reconfigured: the old count means nothing
	}
	if age := now.Sub(s.SavedAt); age < 0 || age > stateMaxAge {
		return e
	}
	if s.SOC < 0 || s.SOC > 1 || math.IsNaN(s.SOC) {
		return e
	}
	e.soc, e.initialised, e.tracking, e.savedSOC = s.SOC, true, true, s.SOC
	if s.Eta >= 0.5 && s.Eta <= 1 {
		e.eta = s.Eta
	}
	return e
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

func (e *Estimator) avgCell(s Sample) float64 {
	if len(s.Cells) > 0 {
		var sum float64
		for _, c := range s.Cells {
			sum += c
		}
		return sum / float64(len(s.Cells))
	}
	return s.VBat / float64(e.p.Cells)
}

func (e *Estimator) minCell(s Sample) float64 {
	if len(s.Cells) == 0 {
		return e.avgCell(s)
	}
	m := s.Cells[0]
	for _, c := range s.Cells[1:] {
		m = math.Min(m, c)
	}
	return m
}

func ema(prev, v float64, have bool, dt time.Duration) float64 {
	if !have || dt <= 0 {
		return v
	}
	a := dt.Seconds() / (smoothTau.Seconds() + dt.Seconds())
	return prev + a*(v-prev)
}

// Update feeds one sample and returns the current estimate.
func (e *Estimator) Update(s Sample) Estimate {
	vc := e.avgCell(s)

	var dt time.Duration
	if e.haveLast {
		dt = s.At.Sub(e.last)
	}
	e.last, e.haveLast = s.At, true
	gap := dt <= 0 || dt > gapMax

	if !e.initialised {
		e.soc, e.initialised, e.tracking = e.p.SOCFromCell(vc), true, false
	} else if !gap {
		net := s.ChargeA*chargeEff - s.DischargeA
		e.soc = clamp01(e.soc + net*dt.Hours()/e.p.CapacityAh)
	}
	if gap {
		dt = 0 // do not let a gap act as a long smoothing step
	}

	// Anchor: end of a constant-voltage charge means full.
	if !s.OnBattery && s.Charging && vc >= e.p.BulkStop-cvMargin {
		e.sawCV = true
	}
	if s.OnBattery {
		e.sawCV = false
	}
	if e.sawCV && !s.OnBattery && !s.Charging && s.ChargeA < minCurrent {
		e.soc, e.tracking, e.sawCV = 1, true, false
	}

	// Anchor: the UPS is about to cut off, the pack is empty.
	if s.OnBattery && e.minCell(s) <= e.p.CutoffCell+0.02 {
		e.soc, e.tracking = 0, true
	}

	// Anchor: at rest, the voltage is the open-circuit voltage. Only the steep
	// low end of the curve is used; elsewhere a single ADC step is many percent.
	rest := math.Max(s.ChargeA, s.DischargeA) < restCurrent
	switch {
	case !rest:
		e.resting = false
	case !e.resting:
		e.resting, e.restSince = true, s.At
	case s.At.Sub(e.restSince) >= restMin && dt > 0:
		if ocv := e.p.SOCFromCell(vc); ocv < ocvMaxSOC {
			w := 1 - math.Exp(-dt.Seconds()/smoothTau.Seconds())
			e.soc = clamp01(e.soc + w*(ocv-e.soc))
			e.tracking = true
		}
	}

	// Smoothed load, and efficiency learned while discharging.
	e.avgP, e.haveAvgP = ema(e.avgP, s.OutputW, e.haveAvgP, dt), true
	if s.OnBattery {
		e.avgI, e.haveAvgI = ema(e.avgI, s.DischargeA, e.haveAvgI, dt), true
		if s.DischargeA > 0.5 && s.OutputW > 5 && s.VBat > 0 {
			sample := math.Max(0.5, math.Min(1, s.OutputW/(s.VBat*s.DischargeA)))
			e.eta += etaAlpha * (sample - e.eta)
		}
	} else {
		e.haveAvgI = false
	}

	est := Estimate{ChargePct: e.soc * 100, Tracking: e.tracking}
	remaining := e.soc * e.p.CapacityAh // Ah
	switch {
	case e.soc <= 0:
		est.Runtime, est.RuntimeOK = 0, s.OnBattery || e.haveAvgP
	case s.OnBattery:
		if e.avgI >= minCurrent {
			est.Runtime, est.RuntimeOK = hours(remaining/e.avgI), true
		}
	default:
		if e.avgP >= minPowerW {
			v := float64(e.p.Cells) * e.p.nominalCell()
			i := e.avgP / (v * e.eta)
			est.Runtime, est.RuntimeOK = hours(remaining/i), true
		}
	}
	if est.Runtime > maxRuntime {
		est.Runtime = maxRuntime
	}
	return est
}

func hours(h float64) time.Duration { return time.Duration(h * float64(time.Hour)) }

// Efficiency is the learned output/battery power ratio (for tests and logs).
func (e *Estimator) Efficiency() float64 { return e.eta }

// SaveIfDue writes the state file at most once a minute, and only when the
// charge moved, to keep flash wear on small boards negligible.
func (e *Estimator) SaveIfDue(now time.Time) error {
	if e.statePath == "" || !e.initialised {
		return nil
	}
	if !e.lastSave.IsZero() && now.Sub(e.lastSave) < saveEvery {
		return nil
	}
	return e.save(now, false)
}

// Save writes the state unconditionally (used at shutdown).
func (e *Estimator) Save(now time.Time) error {
	if e.statePath == "" || !e.initialised {
		return nil
	}
	return e.save(now, true)
}

func (e *Estimator) save(now time.Time, force bool) error {
	if !force && math.Abs(e.soc-e.savedSOC) < 0.002 {
		e.lastSave = now
		return nil
	}
	b, err := json.Marshal(savedState{Version: stateFormat, SOC: e.soc, Eta: e.eta, SavedAt: now,
		CapacityAh: e.p.CapacityAh, Cells: e.p.Cells})
	if err != nil {
		return err
	}
	tmp := e.statePath + ".tmp"
	if err := os.MkdirAll(filepath.Dir(e.statePath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, e.statePath); err != nil {
		return err
	}
	e.lastSave, e.savedSOC = now, e.soc
	return nil
}

// EstimateSensors are the entities added when the estimator is active.
func EstimateSensors() []Sensor {
	return []Sensor{
		{Slug: "battery_charge", Name: "Battery Charge", NUT: "battery.charge", Unit: "%", Class: "battery", StateClass: "measurement", Icon: "mdi:battery-heart-variant", Precision: 0},
		{Slug: "runtime", Name: "Runtime Estimate", NUT: "battery.runtime", Unit: "s", Class: "duration", StateClass: "measurement", Icon: "mdi:timer-sand", Precision: 0},
		{Slug: "estimate_state", Name: "Charge Estimate", Icon: "mdi:information-outline", Category: "diagnostic", Text: true},
	}
}

// SampleFromReading extracts a Sample from an OpenUPS reading. ok is false
// when a value the estimator depends on is missing.
func SampleFromReading(r Reading, now time.Time) (Sample, bool) {
	s := Sample{At: now}
	var ok bool
	if s.VBat, ok = r.Num["vbat"]; !ok {
		return s, false
	}
	for i := 1; ; i++ {
		v, has := r.Num[fmt.Sprintf("vcell_%d", i)]
		if !has {
			break
		}
		s.Cells = append(s.Cells, v)
	}
	s.ChargeA = r.Num["c_charge"]
	s.DischargeA = r.Num["c_discharge"]
	s.OutputW = r.Num["output_power"]
	s.OnBattery = strings.HasPrefix(r.Status, "OB")
	s.Charging = strings.Contains(r.Status, "CHRG")
	return s, true
}

// Apply updates the estimator from a reading and adds the estimated values to
// it (battery_charge, runtime, estimate_state). Readings without the needed
// values are left alone.
func (e *Estimator) Apply(r *Reading, now time.Time) {
	s, ok := SampleFromReading(*r, now)
	if !ok {
		return
	}
	// A pack with fewer cells than the sensors report would skew the average.
	if len(s.Cells) > e.p.Cells {
		s.Cells = s.Cells[:e.p.Cells]
	}
	est := e.Update(s)
	r.SetNum("battery_charge", math.Round(est.ChargePct))
	if est.RuntimeOK {
		r.SetNum("runtime", math.Round(est.Runtime.Seconds()))
	}
	if est.Tracking {
		r.SetStr("estimate_state", "tracking")
	} else {
		r.SetStr("estimate_state", "initial, from voltage")
	}
}

// WithSensors returns a copy of the device with extra sensors appended.
func (d *Device) WithSensors(extra ...Sensor) *Device {
	c := *d
	c.Sensors = append(append([]Sensor(nil), d.Sensors...), extra...)
	return &c
}
