package evaluation

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/Alpenl/cairn-x-enricher/internal/taxonomy"
)

func TestResourceKindsScoringCountsErrorsAndLeavesHistoricalUnknown(t *testing.T) {
	a, b, c := gold(nil, "", ""), gold(nil, "", ""), gold(nil, "", "")
	a.ResourceKinds = &Label{Values: []string{"software"}}
	b.ResourceKinds = &Label{NotApplicable: true}
	pa, pb, pc := prediction("a", nil, "", "", nil), prediction("b", nil, "", "", nil), prediction("c", nil, "", "", nil)
	pa.ResourceKinds, pb.ResourceKinds, pc.ResourceKinds = []string{"prompt"}, []string{"software"}, []string{"software"}
	d := Dataset{Name: "resource-fixture", Split: "test", Samples: []Sample{sample("a", a), sample("b", b), sample("c", c)}, Prediction: []Prediction{pa, pb, pc}}
	r, err := Score(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range r.Dimensions {
		if m.Dimension == "resource_kinds" {
			if m.FalsePos != 2 || m.FalseNeg != 1 || m.Support != 2 || m.Unknown != 1 {
				t.Fatalf("lost resource errors or invented historical negative: %+v", m)
			}
			return
		}
	}
	t.Fatal("resource metric missing")
}

func TestResourceKindsReplayCalibrationAndFitUseSavedNouls(t *testing.T) {
	catalog := exportCatalog()
	catalog.ResourceKinds = []taxonomy.Term{{ID: "software", Label: "软件", Description: "可使用的软件", Active: true}}
	d, calls := policyDatasetWithCatalog(t, catalog)
	before := calls.Load()
	for i := range d.Samples {
		d.Samples[i].Gold.ResourceKinds = &Label{Values: []string{"software"}}
	}
	v, err := PreparePolicyReplay(d, catalog, "jev-1.13.0")
	if err != nil {
		t.Fatal(err)
	}
	r, err := v.Calibration(calibrationConfig())
	if err != nil {
		t.Fatal(err)
	}
	m := probabilityMetric(t, r, "resource_kinds")
	if m.Observations != 2 || m.Brier == nil || math.Abs(*m.Brier-.01) > 1e-12 {
		t.Fatalf("bad resource calibration: %+v", m)
	}
	if len(d.Prediction[0].ResourceKinds) != 1 {
		t.Fatal("replay projection dropped accepted resource")
	}
	config := fitConfig()
	if _, err = FitProductionPolicy(v, config); err == nil {
		t.Fatal("annotated resources silently ignored by legacy fit scope")
	}
	config.MinCoverage["resource_kinds"], config.Risk.DimensionWeights["resource_kinds"] = .5, 1
	fitted, err := FitProductionPolicy(v, config)
	if err != nil || fitted.Selected == nil {
		t.Fatalf("modern resource fit: selected=%v err=%v", fitted.Selected, err)
	}
	if calls.Load() != before || fitted.ModelCalls != 0 {
		t.Fatal("offline resource fit called model")
	}
	for i := range d.Samples {
		d.Samples[i].Gold.ResourceKinds = nil
	}
	v, err = PreparePolicyReplay(d, catalog, "jev-1.13.0")
	if err != nil {
		t.Fatal(err)
	}
	r, err = v.Calibration(calibrationConfig())
	if err != nil {
		t.Fatal(err)
	}
	m = probabilityMetric(t, r, "resource_kinds")
	if m.Brier != nil || m.UnknownReference != 2 {
		t.Fatalf("historical resources became negative calibration: %+v", m)
	}
}

func TestHistoricalGoldSerializationOmitsUnannotatedResourceKinds(t *testing.T) {
	encoded, err := json.Marshal(gold(nil, "", ""))
	if err != nil || strings.Contains(string(encoded), "resource_kinds") {
		t.Fatalf("historical reference identity changed: %s err=%v", encoded, err)
	}
}

func TestResourceKindsProductionExportPreservesMachineProvenance(t *testing.T) {
	catalog := exportCatalog()
	catalog.ResourceKinds = []taxonomy.Term{{ID: "software", Label: "软件", Description: "可使用的软件", Active: true}}
	d, err := ExportDataset(context.Background(), exportFixtureWithCatalog(t, catalog), ExportOptions{LinkIDs: []int64{7}})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Prediction[0].ResourceKinds) != 1 || d.Prediction[0].ResourceKinds[0] != "software" || d.Samples[0].Gold != nil || d.Samples[0].Provenance != ProvenanceMachinePrediction {
		t.Fatal("resource export dropped proposal or fabricated gold")
	}
}
