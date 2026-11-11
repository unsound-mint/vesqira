package runs

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Controls are the facts about how a result was smoothed, how its noise was modelled and how a control (a null) was built
// (ADR 0054). They are declared by the researcher, never read out of a file: each block says who stated it and when, and a
// block Vesqira built itself (the null of a relative-orientation analysis) is the only one labelled verified. A missing
// block means nothing was stated, which is not "none was applied".
type Controls struct {
	Smoothing    *Smoothing    `json:"smoothing"`
	Noise        *NoiseModel   `json:"noise"`
	Construction *Construction `json:"construction"`
}

// Stated says who is responsible for a block: basis is "declared" (a person said so, not checked here) or "verified"
// (Vesqira ran it and records exactly what it ran); by names the author.
type Stated struct {
	Basis string `json:"basis"`
	By    string `json:"by"`
	At    string `json:"at"`
}

// Smoothing is the kernel applied before the result was made. The width needs a unit: an unstated unit is not a number of
// pixels.
type Smoothing struct {
	Kernel string   `json:"kernel"` // gaussian_fwhm | gaussian_sigma | boxcar | other
	Width  *float64 `json:"width"`  // null: smoothing was applied and its width is not stated
	Unit   string   `json:"unit"`   // pixel | arcsec | arcmin | deg | channel | km/s; empty only when Width is null
	Note   string   `json:"note"`
	Stated
}

// NoiseModel is how the noise was described to the analysis.
type NoiseModel struct {
	Kind      string   `json:"kind"`      // scalar_sigma | rms_map | per_channel_rms | other
	Sigma     *float64 `json:"sigma"`     // scalar_sigma only
	SigmaUnit string   `json:"sigmaUnit"` // the unit of Sigma, as written (K, Jy/beam, ...)
	Estimated string   `json:"estimated"` // how the noise was estimated, in words
	Note      string   `json:"note"`
	Stated
}

// ControlParam is one named parameter of a control's construction.
type ControlParam struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Construction is how a control or null was built.
type Construction struct {
	Kind   string         `json:"kind"` // shifted_map_null | injection_test | permutation | other
	Params []ControlParam `json:"params"`
	Note   string         `json:"note"`
	Stated
}

const (
	BasisDeclared = "declared"
	BasisVerified = "verified"
	// ByResearcher is the author of every declared block: the owner of this catalog is the only principal that can write.
	ByResearcher = "researcher"
	ByVesqira    = "vesqira"
)

var (
	smoothKernels = map[string]bool{"gaussian_fwhm": true, "gaussian_sigma": true, "boxcar": true, "other": true}
	smoothUnits   = map[string]bool{"pixel": true, "arcsec": true, "arcmin": true, "deg": true, "channel": true, "km/s": true}
	noiseKinds    = map[string]bool{"scalar_sigma": true, "rms_map": true, "per_channel_rms": true, "other": true}
	constrKinds   = map[string]bool{"shifted_map_null": true, "injection_test": true, "permutation": true, "other": true}
	sigmaUnitRe   = regexp.MustCompile(`^[A-Za-z0-9 ./*^()_-]{1,40}$`)
	paramKeyRe    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
)

const (
	maxText      = 300
	maxParamVal  = 80
	maxCtlParams = 12
)

func checkText(name, v string, max int) error {
	if utf8.RuneCountInString(v) > max {
		return fmt.Errorf("%s is at most %d characters", name, max)
	}
	for _, r := range v {
		if unicode.IsControl(r) && r != '\n' {
			return fmt.Errorf("%s has a control character", name)
		}
	}
	return nil
}

func positive(name string, v *float64) error {
	if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0) || *v <= 0) {
		return fmt.Errorf("%s must be a positive number", name)
	}
	return nil
}

// Declare validates what a person sent and stamps every block as declared by the researcher at now. A block that was not sent
// stays absent. The caller's own basis, author and time are ignored: only Vesqira says "verified".
func (c *Controls) Declare(now time.Time) error {
	at := now.UTC().Format(time.RFC3339)
	st := Stated{Basis: BasisDeclared, By: ByResearcher, At: at}
	if s := c.Smoothing; s != nil {
		s.Stated = st
		s.Kernel, s.Unit, s.Note = strings.TrimSpace(s.Kernel), strings.TrimSpace(s.Unit), strings.TrimSpace(s.Note)
		switch {
		case !smoothKernels[s.Kernel]:
			return fmt.Errorf("smoothing kernel must be gaussian_fwhm, gaussian_sigma, boxcar or other")
		case positive("smoothing width", s.Width) != nil:
			return positive("smoothing width", s.Width)
		case s.Width != nil && !smoothUnits[s.Unit]:
			return fmt.Errorf("a smoothing width needs its unit: pixel, arcsec, arcmin, deg, channel or km/s")
		case s.Width == nil && s.Unit != "":
			return fmt.Errorf("a unit without a width: give the width, or leave the unit out")
		case s.Kernel == "other" && s.Note == "":
			return fmt.Errorf("describe a kernel of kind other in the note")
		}
		if err := checkText("smoothing note", s.Note, maxText); err != nil {
			return err
		}
	}
	if n := c.Noise; n != nil {
		n.Stated = st
		n.Kind, n.SigmaUnit = strings.TrimSpace(n.Kind), strings.TrimSpace(n.SigmaUnit)
		n.Estimated, n.Note = strings.TrimSpace(n.Estimated), strings.TrimSpace(n.Note)
		switch {
		case !noiseKinds[n.Kind]:
			return fmt.Errorf("noise model must be scalar_sigma, rms_map, per_channel_rms or other")
		case positive("noise sigma", n.Sigma) != nil:
			return positive("noise sigma", n.Sigma)
		case n.Sigma != nil && n.Kind != "scalar_sigma":
			return fmt.Errorf("a sigma value goes with the scalar_sigma model only")
		case n.Sigma != nil && !sigmaUnitRe.MatchString(n.SigmaUnit):
			return fmt.Errorf("a sigma value needs its unit (for example K or Jy/beam)")
		case n.Sigma == nil && n.SigmaUnit != "":
			return fmt.Errorf("a unit without a sigma value: give the value, or leave the unit out")
		case n.Kind == "other" && n.Note == "":
			return fmt.Errorf("describe a noise model of kind other in the note")
		}
		if err := checkText("how the noise was estimated", n.Estimated, maxText); err != nil {
			return err
		}
		if err := checkText("noise note", n.Note, maxText); err != nil {
			return err
		}
	}
	if k := c.Construction; k != nil {
		k.Stated = st
		k.Kind, k.Note = strings.TrimSpace(k.Kind), strings.TrimSpace(k.Note)
		if !constrKinds[k.Kind] {
			return fmt.Errorf("control construction must be shifted_map_null, injection_test, permutation or other")
		}
		if k.Kind == "other" && k.Note == "" {
			return fmt.Errorf("describe a construction of kind other in the note")
		}
		if len(k.Params) > maxCtlParams {
			return fmt.Errorf("a construction has at most %d parameters", maxCtlParams)
		}
		seen := map[string]bool{}
		for i := range k.Params {
			p := &k.Params[i]
			p.Key, p.Value = strings.TrimSpace(p.Key), strings.TrimSpace(p.Value)
			if !paramKeyRe.MatchString(p.Key) || seen[p.Key] {
				return fmt.Errorf("parameter names are lower-case words (letters, digits, _), each once: %q", p.Key)
			}
			seen[p.Key] = true
			if err := checkText("parameter "+p.Key, p.Value, maxParamVal); err != nil {
				return err
			}
		}
		if k.Params == nil {
			k.Params = []ControlParam{}
		}
		if err := checkText("construction note", k.Note, maxText); err != nil {
			return err
		}
	}
	return nil
}

// Empty says no block is stated.
func (c *Controls) Empty() bool {
	return c == nil || (c.Smoothing == nil && c.Noise == nil && c.Construction == nil)
}
