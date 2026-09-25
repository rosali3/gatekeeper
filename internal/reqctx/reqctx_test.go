package reqctx

import (
	"context"
	"testing"
)

func TestWithAccessFields_RoundTrip(t *testing.T) {
	ctx, fields := WithAccessFields(context.Background())
	fields.Route = "/api/"
	fields.Upstream = "floorplan"

	got := AccessFieldsFrom(ctx)
	if got != fields {
		t.Fatal("AccessFieldsFrom did not return the same pointer stored by WithAccessFields")
	}
	if got.Route != "/api/" || got.Upstream != "floorplan" {
		t.Errorf("got %+v, want Route=/api/ Upstream=floorplan", got)
	}
}

func TestAccessFieldsFrom_Missing(t *testing.T) {
	if got := AccessFieldsFrom(context.Background()); got != nil {
		t.Errorf("AccessFieldsFrom(bare context) = %v, want nil", got)
	}
}
